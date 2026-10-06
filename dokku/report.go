package dokku

import (
	"bufio"
	"strings"
)

// parseReports lê a saída de `dokku <plugin>:report` sem app, que traz uma
// seção por app:
//
//	=====> api ps information
//	       Deployed:                      true
//
// e devolve app → chave em minúsculas ("deployed") → valor.
func parseReports(out string) map[string]map[string]string {
	reports := map[string]map[string]string{}
	var current map[string]string
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if header, ok := strings.CutPrefix(line, "=====>"); ok {
			fields := strings.Fields(header)
			if len(fields) == 0 {
				current = nil
				continue
			}
			current = map[string]string{}
			reports[fields[0]] = current
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		if current == nil || !ok {
			continue
		}
		current[strings.ToLower(strings.TrimSpace(key))] = strings.TrimSpace(value)
	}
	return reports
}

// parseList lê a saída de comandos como `apps:list`, ignorando o cabeçalho.
func parseList(out string) []string {
	var items []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "=====>") {
			continue
		}
		items = append(items, line)
	}
	return items
}

// appContainers resume os containers de deploy de uma app.
type appContainers struct {
	processes  map[string]int // tipo de processo → containers rodando
	starting   bool           // criado ou com healthcheck ainda "starting"
	restarting bool           // docker reiniciando o container (restart policy)
	removing   bool
}

// parseContainers lê a saída do `docker ps` formatada como
// "<app> <tipo> <estado> <status...>".
func parseContainers(out string) map[string]*appContainers {
	apps := map[string]*appContainers{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		app, typ, state, detail := fields[0], fields[1], fields[2], strings.Join(fields[3:], " ")
		c := apps[app]
		if c == nil {
			c = &appContainers{processes: map[string]int{}}
			apps[app] = c
		}
		switch state {
		case "running":
			if strings.Contains(detail, "health: starting") {
				c.starting = true
				continue
			}
			c.processes[typ]++
		case "created":
			c.starting = true
		case "restarting":
			c.restarting = true
		case "removing", "dead":
			c.removing = true
		}
		if _, ok := c.processes[typ]; !ok {
			c.processes[typ] = 0
		}
	}
	return apps
}
