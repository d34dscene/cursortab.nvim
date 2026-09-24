package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync/atomic"
	"time"

	"cursortab/buffer"
	"cursortab/ctx"
	"cursortab/engine"
	"cursortab/index"
	"cursortab/logger"
	"cursortab/provider"
	"cursortab/types"

	"github.com/neovim/go-client/nvim"
)

const probeTimeout = 5 * time.Second

type Daemon struct {
	config      Config
	provider    engine.Provider
	buffer      *buffer.NvimBuffer
	engine      *engine.Engine
	listener    net.Listener
	pidPath     string
	clientCount int64
	ctx         context.Context
	cancel      context.CancelFunc
}

func newDaemonContext() (context.Context, context.CancelFunc) {
	return context.WithCancel(context.Background())
}

func NewDaemon(config Config) (*Daemon, error) {
	daemonCtx, cancel := newDaemonContext()

	providerConfig, err := buildProviderConfig(daemonCtx, &config)
	if err != nil {
		cancel()
		return nil, err
	}

	buf := buffer.New(buffer.Config{
		NsID: config.NsID,
	})

	prov, err := provider.Build(provider.RoleType, "", providerConfig)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("type provider: %w", err)
	}

	var nextEdit engine.Provider
	if providerConfig.NextEdit != nil {
		editConfig := *providerConfig
		editConfig.Endpoint = *providerConfig.NextEdit
		editConfig.NextEdit = nil
		nextEdit, err = provider.Build(provider.RoleEdit, "", &editConfig)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("next_edit: %w", err)
		}
		if nextEdit.CompletionKind() != engine.CompletionEdit {
			cancel()
			return nil, fmt.Errorf("next_edit model %q is not an edit-prediction provider", providerConfig.NextEdit.Model)
		}
	}

	var retriever ctx.Retriever
	if providerConfig.RetrievalEnabled {
		retriever = index.NewManager(index.Options{})
	}

	nextEditModel := "-"
	if providerConfig.NextEdit != nil {
		nextEditModel = providerConfig.NextEdit.Model
	}
	logger.Info("provider ready: type model=%s context_size=%d next_edit=%s",
		providerConfig.Endpoint.Model, providerConfig.ContextSize, nextEditModel)

	eng, err := engine.NewEngine(prov, buf, engine.EngineConfig{
		NsID:                config.NsID,
		CompletionTimeout:   time.Duration(providerConfig.Endpoint.TimeoutMs) * time.Millisecond,
		NextEditTimeout:     nextEditTimeout(providerConfig),
		IdleCompletionDelay: time.Duration(config.Behavior.IdleCompletionDelay) * time.Millisecond,
		TextChangeDebounce:  time.Duration(config.Behavior.TextChangeDebounce) * time.Millisecond,
		CursorPrediction: engine.CursorPredictionConfig{
			Enabled:            config.Behavior.CursorPrediction.Enabled,
			AutoAdvance:        config.Behavior.CursorPrediction.AutoAdvance,
			ProximityThreshold: config.Behavior.CursorPrediction.ProximityThreshold,
		},
		MaxDiffTokens:      config.Provider.MaxDiffHistoryTokens,
		MaxVisibleLines:    config.Behavior.MaxVisibleLines,
		MaxRetrievalChunks: config.Provider.RetrievalMaxChunks,
		MinConfidence:      config.Provider.MinConfidence,
		CompleteInInsert:   config.Behavior.CompleteInInsert,
		CompleteInNormal:   config.Behavior.CompleteInNormal,
		DisabledIn:         config.Behavior.DisabledIn,
		Retriever:          retriever,
		NextEditProvider:   nextEdit,
	}, engine.SystemClock)
	if err != nil {
		cancel()
		return nil, err
	}

	return &Daemon{
		config:   config,
		provider: prov,
		buffer:   buf,
		engine:   eng,
		pidPath:  getPidPath(config.StateDir),
		ctx:      daemonCtx,
		cancel:   cancel,
	}, nil
}

// buildProviderConfig maps the wire config into the frozen
// types.ProviderConfig and probes each endpoint once to resolve model ids,
// context size, and per-model FIM tokens.
func buildProviderConfig(ctx context.Context, config *Config) (*types.ProviderConfig, error) {
	wire := config.Provider
	providerConfig := &types.ProviderConfig{
		Endpoint: types.EndpointConfig{
			URL:       wire.Endpoint.URL,
			APIKey:    wire.Endpoint.APIKey,
			Model:     wire.Endpoint.Model,
			MaxTokens: wire.Endpoint.MaxTokens,
			TimeoutMs: wire.Endpoint.TimeoutMs,
		},
		ContextSize:        wire.ContextSize,
		RetrievalEnabled:   wire.RetrievalEnabled,
		RetrievalMaxChunks: wire.RetrievalMaxChunks,
		Logprobs:           wire.Logprobs,
		MinConfidence:      wire.MinConfidence,
	}
	if wire.FIMTokens != nil {
		providerConfig.FIMTokens = &types.FIMTokenConfig{
			Prefix:      wire.FIMTokens.Prefix,
			Suffix:      wire.FIMTokens.Suffix,
			Middle:      wire.FIMTokens.Middle,
			RepoName:    wire.FIMTokens.RepoName,
			FileSep:     wire.FIMTokens.FileSep,
			Filename:    wire.FIMTokens.Filename,
			SuffixFirst: wire.FIMTokens.SuffixFirst,
		}
	}
	if wire.NextEdit != nil {
		providerConfig.NextEdit = &types.EndpointConfig{
			URL:       wire.NextEdit.URL,
			APIKey:    wire.NextEdit.APIKey,
			Model:     wire.NextEdit.Model,
			MaxTokens: wire.NextEdit.MaxTokens,
			TimeoutMs: wire.NextEdit.TimeoutMs,
		}
	}

	typeRes, err := resolveEndpoint(ctx, &providerConfig.Endpoint, provider.RoleType)
	if err != nil {
		return nil, err
	}
	if providerConfig.ContextSize == 0 {
		if typeRes != nil && typeRes.ContextSize > 0 {
			providerConfig.ContextSize = typeRes.ContextSize
		} else {
			providerConfig.ContextSize = defaultContextSize
		}
	}
	if providerConfig.FIMTokens == nil && typeRes != nil {
		providerConfig.FIMTokens = typeRes.FIMTokens[providerConfig.Endpoint.Model]
	}

	if providerConfig.NextEdit != nil {
		editEndpoint := providerConfig.NextEdit
		var editRes *provider.Resolved
		if editEndpoint.URL == providerConfig.Endpoint.URL {
			editRes = typeRes
		}
		if editRes == nil {
			editRes, err = resolveEndpoint(ctx, editEndpoint, provider.RoleEdit)
			if err != nil {
				return nil, err
			}
		} else if editEndpoint.Model == "" || editEndpoint.Model == "auto" {
			editEndpoint.Model = provider.ResolveModel(provider.RoleEdit, editRes)
			if editEndpoint.Model == "" || editEndpoint.Model == "auto" {
				return nil, fmt.Errorf("no edit model available at %s", editEndpoint.URL)
			}
		}
	}

	return providerConfig, nil
}

// resolveEndpoint probes one endpoint and resolves its model id for the role.
// A probe failure is fatal only when the model still needs resolving ("auto"
// or empty). An explicitly pinned model keeps working with a warning.
func resolveEndpoint(ctx context.Context, endpoint *types.EndpointConfig, role provider.Role) (*provider.Resolved, error) {
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	res, err := provider.Probe(probeCtx, endpoint)
	if err != nil {
		pinned := endpoint.Model != "" && endpoint.Model != "auto"
		if !pinned {
			return nil, fmt.Errorf("probe %s: cannot resolve model %q: %w", endpoint.URL, endpoint.Model, err)
		}
		logger.Warn("probe %s failed, using configured model %q: %v", endpoint.URL, endpoint.Model, err)
		return nil, nil
	}

	if endpoint.Model == "" || endpoint.Model == "auto" {
		endpoint.Model = provider.ResolveModel(role, res)
		if endpoint.Model == "" || endpoint.Model == "auto" {
			return nil, fmt.Errorf("no %s model available at %s", role, endpoint.URL)
		}
	}
	return res, nil
}

func nextEditTimeout(providerConfig *types.ProviderConfig) time.Duration {
	if providerConfig.NextEdit == nil {
		return 0
	}
	return time.Duration(providerConfig.NextEdit.TimeoutMs) * time.Millisecond
}

func (d *Daemon) Start() error {
	// Setup logging and PID management
	d.writePidFile()
	defer d.removePidFile()

	// Setup IPC
	listener, addr, err := listenIPC(d.config.StateDir)
	if err != nil {
		return err
	}
	d.listener = listener
	defer cleanupIPC(d.config.StateDir)

	logger.Info("daemon listening on: %s", addr)

	// Start engine
	d.engine.Start(d.ctx)

	// Setup shutdown handling
	d.setupShutdownHandling()

	// Start connection handling
	go d.acceptConnections()

	// Start idle monitoring
	go d.monitorIdleShutdown()

	// Wait for shutdown
	<-d.ctx.Done()
	logger.Info("daemon shutting down...")
	return nil
}

func (d *Daemon) setupShutdownHandling() {
	setupShutdownHandler(func() {
		logger.Info("received shutdown signal")
		d.Stop()
	})
}

func (d *Daemon) acceptConnections() {
	for {
		conn, err := d.listener.Accept()
		if err != nil {
			select {
			case <-d.ctx.Done():
				return // Server is shutting down
			default:
				logger.Error("error accepting connection: %v", err)
				continue
			}
		}

		atomic.AddInt64(&d.clientCount, 1)
		logger.Info("new client connected, total clients: %d", atomic.LoadInt64(&d.clientCount))
		go d.handleConnection(conn)
	}
}

func (d *Daemon) handleConnection(conn net.Conn) {
	defer conn.Close()
	defer func() {
		atomic.AddInt64(&d.clientCount, -1)
		logger.Info("client disconnected, remaining clients: %d", atomic.LoadInt64(&d.clientCount))
	}()

	// Create Neovim client from the connection
	n, err := nvim.New(conn, conn, conn, logger.Debug)
	if err != nil {
		logger.Error("error creating nvim client: %v", err)
		return
	}

	// Set nvim client on the buffer and register event handler
	d.buffer.SetClient(n)
	d.engine.RegisterEventHandler()

	// Serve this connection until it closes or context is done
	select {
	case <-d.ctx.Done():
		return
	default:
		if err := n.Serve(); err != nil && err != io.EOF {
			logger.Error("error serving connection: %v", err)
		}
	}
}

func (d *Daemon) monitorIdleShutdown() {
	// In debug mode, shut down immediately when no clients are connected
	if d.config.Debug.ImmediateShutdown {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-d.ctx.Done():
				return
			case <-ticker.C:
				if atomic.LoadInt64(&d.clientCount) == 0 {
					logger.Debug("debug mode: no clients connected, shutting down daemon immediately")
					d.Stop()
					return
				}
			}
		}
	} else {
		// Normal mode: wait for timeout period before shutting down
		idleTimer := time.NewTimer(30 * time.Second)
		defer idleTimer.Stop()

		for {
			select {
			case <-d.ctx.Done():
				return
			case <-idleTimer.C:
				if atomic.LoadInt64(&d.clientCount) == 0 {
					logger.Info("no clients connected for timeout period, shutting down daemon")
					d.Stop()
					return
				}
			}

			// Reset timer when no clients
			if atomic.LoadInt64(&d.clientCount) == 0 {
				idleTimer.Reset(5 * time.Second)
			} else {
				idleTimer.Reset(30 * time.Second)
			}
		}
	}
}

func (d *Daemon) Stop() {
	d.engine.Stop()
	d.cancel()
	if d.listener != nil {
		d.listener.Close()
	}
}

func (d *Daemon) writePidFile() {
	pid := os.Getpid()
	err := os.WriteFile(d.pidPath, []byte(strconv.Itoa(pid)), 0644)
	if err != nil {
		logger.Warn("could not write PID file: %v", err)
	}
	logger.Info("server started with PID %d", pid)
}

func (d *Daemon) removePidFile() {
	if err := os.Remove(d.pidPath); err != nil && !os.IsNotExist(err) {
		logger.Warn("could not remove PID file: %v", err)
	}
}
