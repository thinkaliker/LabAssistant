// Package modules assembles the set of capabilities compiled into the associate (and its
// privileged helper). Adding a module = importing it here.
package modules

import (
	"github.com/thinkaliker/labassistant/module"
	"github.com/thinkaliker/labassistant/modules/duo"
	"github.com/thinkaliker/labassistant/modules/qup"
	"github.com/thinkaliker/labassistant/modules/sys"
)

// Default returns the built-in module set.
func Default() []module.Module {
	return []module.Module{
		duo.New(),
		qup.New(),
		sys.New(),
	}
}
