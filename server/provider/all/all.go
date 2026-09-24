// Package all pulls in the provider registry for historical blank imports.
// Registration lives inside provider itself, so importing it here is what a
// side-effect import needs to keep working.
package all

import _ "cursortab/provider"
