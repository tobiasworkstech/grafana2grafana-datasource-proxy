package main

import (
	"os"

	"github.com/grafana/grafana-plugin-sdk-go/backend/datasource"
	"github.com/grafana/grafana-plugin-sdk-go/backend/log"

	"github.com/tobiasworkstech/grafana2grafana-datasource-proxy/pkg/plugin"
)

func main() {
	if err := datasource.Manage("tobiasworkstech-grafana2grafanaproxy-datasource", plugin.NewDatasource, datasource.ManageOpts{}); err != nil {
		log.DefaultLogger.Error(err.Error())
		os.Exit(1)
	}
}
