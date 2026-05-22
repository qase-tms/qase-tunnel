package buildinfo

var (
	Version      = "dev"
	APIBaseURL   = "https://api.qase.io"
	FrpsServer   = "frps.qase.io"
	FrpsTCPPort  = "0"
	FrpsQUICPort = "0"
)

const (
	PathTunnelsList     = "/v2/tunnels"
	PathTunnelRegister  = "/v2/tunnels"
	PathTunnelRotateFmt = "/v2/tunnels/%s/rotate"
	PathTunnelHeartbeat = "/v2/tunnels/%s/heartbeat"
)
