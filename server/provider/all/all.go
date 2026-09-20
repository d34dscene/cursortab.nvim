// Package all imports every config-buildable provider so their init
// registrations run. Import it for side effects wherever a provider is built
// from a type name.
package all

import (
	_ "cursortab/provider/fim"
	_ "cursortab/provider/inline"
	_ "cursortab/provider/mercuryapi"
	_ "cursortab/provider/sweep"
	_ "cursortab/provider/zeta"
	_ "cursortab/provider/zeta2"
)
