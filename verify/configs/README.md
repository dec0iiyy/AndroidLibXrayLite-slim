# Config fixtures

Every `*.json` here has to load and start on the full upstream Xray-core **and** on the slim build, or the release stops. Add the shape of the configs your app really sends: the protocols, transports, `mux`, balancers, observatories and routing rules it uses. Replace secrets with placeholders (UUIDs, keys, passwords) and addresses with documentation ranges (`198.51.100.0/24`, `example.com`): nothing here dials out.

- `geosite:` and `geoip:` work: `verify` writes small `geosite.dat` / `geoip.dat` files with the codes listed in `geo.go` and points Xray at them.
- Listening ports are replaced with free ones before the start.
- A config that starts but makes Xray log that part of it "is ignored" or "has been removed" fails too: it would run, but not the way the app expects (for example the outbound `mux` setting, which Xray-core ignores since #7364). Warnings about deprecated settings are only printed.
- If upstream itself rejects a fixture, the build stops before the slim patch with `upstream:` in the log. That is an upstream change your app's configs have to follow, not a slim problem.
