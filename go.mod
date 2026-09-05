module github.com/pion/ice/v4

go 1.24.0

replace github.com/pion/dtls/v3 => github.com/pion/dtls/v3 v3.1.3-0.20260902001837-a2624993668b

require (
	github.com/google/uuid v1.6.0
	github.com/pion/dtls/v3 v3.1.8
	github.com/pion/logging v0.2.4
	github.com/pion/mdns/v2 v2.2.0
	github.com/pion/randutil v0.1.0
	github.com/pion/stun/v4 v4.0.1-0.20260903164631-9ebdd2632757
	github.com/pion/transport/v4 v4.1.0
	github.com/pion/turn/v5 v5.1.0
	github.com/stretchr/testify v1.12.1
	golang.org/x/net v0.49.0
)

require (
	github.com/wlynxg/anet v0.0.5 // indirect
	go.yaml.in/yaml/v3 v3.0.5 // indirect
	golang.org/x/crypto v0.48.0 // indirect
	golang.org/x/sys v0.41.0 // indirect
	golang.org/x/time v0.14.0 // indirect
)
