module sa_cmd

go 1.25.0

require (
	github.com/BurntSushi/toml v1.6.0
	solar_assistant v0.0.0-00010101000000-000000000000
)

require (
	github.com/gorilla/websocket v1.5.3 // indirect
	golang.org/x/sys v0.42.0 // indirect
	golang.org/x/term v0.41.0 // indirect
)

replace solar_assistant => ../go_solar_assistant
