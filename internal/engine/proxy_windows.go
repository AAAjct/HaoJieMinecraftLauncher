package engine

import (
	"net/http"
	"net/url"
	"os"
	"strings"

	"golang.org/x/net/http/httpproxy"
	"golang.org/x/sys/windows/registry"
)

func launcherProxy(req *http.Request) (*url.URL, error) {
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY"} {
		if os.Getenv(name) != "" {
			return http.ProxyFromEnvironment(req)
		}
	}
	key, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return nil, nil
	}
	defer key.Close()
	enabled, _, _ := key.GetIntegerValue("ProxyEnable")
	if enabled != 1 {
		return nil, nil
	}
	server, _, _ := key.GetStringValue("ProxyServer")
	bypass, _, _ := key.GetStringValue("ProxyOverride")
	return windowsProxy(server, bypass, req.URL)
}

func windowsProxy(server, bypass string, target *url.URL) (*url.URL, error) {
	config := httpproxy.Config{}
	if !strings.Contains(server, "=") {
		config.HTTPProxy, config.HTTPSProxy = server, server
	} else {
		for _, entry := range strings.Split(server, ";") {
			scheme, address, _ := strings.Cut(entry, "=")
			switch strings.ToLower(strings.TrimSpace(scheme)) {
			case "http":
				config.HTTPProxy = strings.TrimSpace(address)
			case "https":
				config.HTTPSProxy = strings.TrimSpace(address)
			}
		}
	}
	for _, entry := range strings.Split(bypass, ";") {
		entry = strings.TrimSpace(entry)
		if strings.EqualFold(entry, "<local>") && !strings.Contains(target.Hostname(), ".") {
			return nil, nil
		}
	}
	config.NoProxy = strings.ReplaceAll(bypass, ";", ",")
	return config.ProxyFunc()(target)
}
