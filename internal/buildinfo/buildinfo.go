package buildinfo

import "runtime"

var Version = "dev"
var Commit = "unknown"

type Info struct {
	Version  string `json:"version"`
	Commit   string `json:"commit"`
	Platform string `json:"platform"`
	Arch     string `json:"arch"`
}

func Current() Info { return Info{Version, Commit, runtime.GOOS, runtime.GOARCH} }
