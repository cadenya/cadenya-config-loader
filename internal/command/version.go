package command

import (
	"runtime/debug"
	"strings"
)

// Version returns the release version. Release builds set it through ldflags;
// `go install ...@vX.Y.Z` doesn't, but Go records the module version in the
// binary, so fall back to that before reporting a development build.
func Version(ldflags string) string {
	if ldflags != "" && ldflags != "dev" {
		return ldflags
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return strings.TrimPrefix(v, "v")
		}
	}
	return "dev"
}
