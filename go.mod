module github.com/baragoon/webhook

go 1.27.4

require (
	github.com/Microsoft/go-winio v0.6.4
	github.com/clbanning/mxj/v2 v2.7.0
	github.com/coreos/go-systemd/v22 v22.7.0
	github.com/dustin/go-humanize v1.1.0
	github.com/fsnotify/fsnotify v1.10.1
	github.com/go-chi/chi/v5 v5.5.0
	github.com/gofrs/uuid/v5 v5.5.1
	github.com/gorilla/mux v1.8.1
	golang.org/x/sys v0.50.0
	sigs.k8s.io/yaml v1.6.0
)

require (
	github.com/google/go-cmp v0.6.0 // indirect
	go.yaml.in/yaml/v2 v2.4.2 // indirect
)

replace go.yaml.in/yaml/v2 => ./third_party/go-yaml

replace sigs.k8s.io/yaml => ./third_party/sigs-yaml
