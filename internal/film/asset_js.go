//go:build js

package film

import (
	"embed"
	"errors"
	"sync"
	"syscall/js"
)

// The browser build embeds the base pair only. The Chinese atlases are a
// separate download: web/game.js fetches them when the engine asks, so they
// stay out of the initial payload and out of the wasm binary.
//
//go:embed assets/display.png assets/display.json assets/body.png assets/body.json
var baseAssets embed.FS

var (
	cjkMu    sync.Mutex
	cjkBytes = map[string][]byte{}
)

// cjkAssets are the four files the host delivers, in any order.
var cjkAssets = []string{"display-cjk.png", "display-cjk.json", "body-cjk.png", "body-cjk.json"}

func failCJK(err error) {
	cjkFailure.Store(&err)
	cjkEnabled.Store(false)
}

func init() {
	js.Global().Set("nanolatheFilmCJKFailed", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) == 1 && args[0].Type() == js.TypeString {
			failCJK(errors.New(args[0].String()))
		}
		return nil
	}))
	js.Global().Set("nanolatheFilmCJKReady", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) != 2 || args[0].Type() != js.TypeString {
			return nil
		}
		name := args[0].String()
		data := make([]byte, args[1].Get("length").Int())
		js.CopyBytesToGo(data, args[1])
		ready := false
		cjkMu.Lock()
		cjkBytes[name] = data
		ready = true
		for _, want := range cjkAssets {
			if _, ok := cjkBytes[want]; !ok {
				ready = false
				break
			}
		}
		cjkMu.Unlock()
		if ready {
			cjkReady.Store(true)
		}
		return nil
	}))
	startCJKFetch = func() {
		loader := js.Global().Get("nanolatheFilmLoadCJK")
		if loader.Type() == js.TypeFunction {
			loader.Invoke()
		} else {
			failCJK(errors.New("browser font loader is unavailable"))
		}
	}
}

func readAsset(name string) ([]byte, error) {
	switch name {
	case "display.png", "display.json", "body.png", "body.json":
		return baseAssets.ReadFile("assets/" + name)
	}
	cjkMu.Lock()
	defer cjkMu.Unlock()
	if data, ok := cjkBytes[name]; ok {
		return data, nil
	}
	return nil, errors.New("film: asset " + name + " has not been fetched")
}
