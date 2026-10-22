//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Valores de dev que casam mock, gateway e cérebro (os mesmos de
// scripts/dev.env.example). Nenhum é segredo.
const (
	chaveMock       = "ik_test_mock"
	segredoWebhook  = "segredo-mock"
	orgID           = "44444444-4444-4444-8444-444444444401"
	tokenTools      = "dev-token"
	deptoHumano     = "Atendimento"
	esperaPadrao    = 15 * time.Second // teto de qualquer espera de um cenário
	assentamento    = 900 * time.Millisecond
	subidaTimeout   = 90 * time.Second
	passoDePolling  = 40 * time.Millisecond
	janelaDebounce  = "300ms"
	tetoDebounce    = "1s"
	intervaloFeed   = "1s"
	atrasoDoCursor  = "1s"
	pausaEntreBolha = "10ms"
)

// pilha é a stack do processo de teste: uma só para todos os cenários, que se
// isolam por telefone e conversa.
var pilha *stack

type stack struct {
	raiz     string // raiz do repositório
	tmp      string
	mockURL  string
	gwURL    string
	cerebro  *cerebroFalso
	proxy    *proxyWebhook
	mock     *proc
	gateway  *proc
	real     *proc // cérebro Python, só com E2E_CEREBRO=real
	cerebroR bool
}

// proc é um processo filho com o log capturado.
type proc struct {
	nome string
	cmd  *exec.Cmd
	log  *bufLog
	fim  chan struct{} // fechado quando o processo termina
}

type bufLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *bufLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *bufLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// cauda devolve as últimas n linhas.
func (l *bufLog) cauda(n int) string {
	linhas := strings.Split(strings.TrimRight(l.String(), "\n"), "\n")
	if len(linhas) > n {
		linhas = linhas[len(linhas)-n:]
	}
	return strings.Join(linhas, "\n")
}

func TestMain(m *testing.M) {
	s, err := subir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: não consegui subir a stack: %v\n", err)
		if s != nil {
			s.despejar()
			s.parar()
		}
		os.Exit(1)
	}
	pilha = s
	code := m.Run()
	s.parar()
	os.Exit(code)
}

func subir() (*stack, error) {
	raiz, err := raizDoRepo()
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "sofia-e2e-")
	if err != nil {
		return nil, err
	}
	s := &stack{raiz: raiz, tmp: tmp, cerebroR: os.Getenv("E2E_CEREBRO") == "real"}

	py, err := acharPython(raiz)
	if err != nil {
		return s, err
	}
	bin := filepath.Join(tmp, "gateway"+exeSufixo())
	build := exec.CommandContext(context.Background(), "go", "build", "-o", bin, "./cmd/bot")
	build.Dir = filepath.Join(raiz, "backend")
	if out, err := build.CombinedOutput(); err != nil {
		return s, fmt.Errorf("go build do gateway falhou: %v\n%s", err, out)
	}

	portaMock, portaGW, portaBrain, err := tresPortas()
	if err != nil {
		return s, err
	}
	s.mockURL = "http://127.0.0.1:" + portaMock
	s.gwURL = "http://127.0.0.1:" + portaGW

	// Cérebro (falso ou real) e o proxy de webhook rodam dentro do teste.
	s.cerebro = novoCerebroFalso(s.gwURL)
	s.proxy = novoProxyWebhook(s.gwURL)
	brainURL := s.cerebro.srv.URL

	// 1) mock-infodesk: webhooks vão para o proxy, que repassa ao gateway.
	s.mock = iniciar("mock", filepath.Join(raiz, "mock-infodesk"), py, []string{"-u", "mock_infodesk.py"}, []string{
		"MOCK_PORT=" + portaMock,
		"MOCK_HOST=127.0.0.1",
		"MOCK_API_KEY=" + chaveMock,
		"MOCK_WEBHOOK_SECRET=" + segredoWebhook,
		"MOCK_ORG_ID=" + orgID,
		"GATEWAY_WEBHOOK_URL=" + s.proxy.srv.URL + "/webhook",
		"MOCK_SEM_REENTREGA=1", // a reentrega que importa é a manual (/mock/reentregar)
	})
	if err := esperarHTTP(s.mockURL+"/mock/config", s.mock, subidaTimeout); err != nil {
		return s, fmt.Errorf("mock-infodesk não subiu: %w", err)
	}

	// 2) cérebro real, se pedido (precisa do venv do agente com as dependências).
	if s.cerebroR {
		if os.Getenv("OPENROUTER_API_KEY") == "" {
			return s, errors.New("E2E_CEREBRO=real exige OPENROUTER_API_KEY no ambiente")
		}
		brainURL = "http://127.0.0.1:" + portaBrain
		s.real = iniciar("cerebro", filepath.Join(raiz, "agente-ia"), py,
			[]string{"-m", "uvicorn", "server:app", "--host", "127.0.0.1", "--port", portaBrain},
			[]string{
				"INFODESK_TOOLS_BASE_URL=" + s.gwURL,
				"INFODESK_TOOLS_TOKEN=" + tokenTools,
				"TAXONOMIA_PATH=" + taxonomiaDev(raiz),
			})
		if err := esperarHTTP(brainURL+"/health", s.real, subidaTimeout); err != nil {
			return s, fmt.Errorf("cérebro real não subiu: %w", err)
		}
	}

	// 3) gateway, contra o mock e o cérebro, com a taxonomia de dev.
	deptoID, err := idDoDepartamento(s.mockURL, deptoHumano)
	if err != nil {
		return s, err
	}
	s.gateway = iniciar("gateway", tmp, bin, nil, ambienteDoGateway(portaGW, brainURL, s.mockURL, raiz, tmp, deptoID))
	if err := esperarHTTP(s.gwURL+"/health", s.gateway, subidaTimeout); err != nil {
		return s, fmt.Errorf("gateway não subiu: %w", err)
	}
	return s, nil
}

func (s *stack) parar() {
	for _, p := range []*proc{s.gateway, s.real, s.mock} {
		p.matar()
	}
	if s.cerebro != nil {
		s.cerebro.srv.Close()
	}
	if s.proxy != nil {
		s.proxy.srv.Close()
	}
	if s.tmp != "" {
		_ = os.RemoveAll(s.tmp)
	}
}

// despejar imprime o fim do log de cada processo (subida que falhou).
func (s *stack) despejar() {
	for _, p := range []*proc{s.mock, s.real, s.gateway} {
		if p != nil {
			fmt.Fprintf(os.Stderr, "--- log %s (últimas linhas) ---\n%s\n", p.nome, p.log.cauda(40))
		}
	}
}

// ambienteDoGateway monta o ambiente do gateway SEM herdar nada de
// configuração do host (um backend/.env ou INFODESK_* do dev não pode mudar o
// resultado): tudo que o config.Load lê é definido aqui ou tirado.
func ambienteDoGateway(porta, brainURL, mockURL, raiz, dir, deptoID string) []string {
	return []string{
		"PORT=" + porta,
		"LOG_LEVEL=info",
		"DB_PATH=" + filepath.Join(dir, "gateway.db"),
		"PG_URL=",
		"ALERTA_WEBHOOK_URL=",
		"WEBHOOK_TOKEN=",
		"PUBLIC_BASE_URL=",
		"BRAIN_BASE_URL=" + brainURL,
		"TOOLS_API_TOKEN=" + tokenTools,
		"INFODESK_BASE_URL=" + mockURL + "/api",
		"INFODESK_API_KEY=" + chaveMock,
		"INFODESK_WEBHOOK_SECRET=" + segredoWebhook,
		"INFODESK_ORGANIZATION_ID=" + orgID,
		"INFODESK_NOME_CHAVE=",
		"HORARIO_HUMANO=", // vazio = sempre dentro do horário: o resultado não depende da hora em que o teste roda (E5.5)
		"TAXONOMIA_PATH=" + taxonomiaDev(raiz),
		"STRICT_TAXONOMIA=true",
		"DEPARTAMENTO_HUMANO_PADRAO=" + deptoID, // o gateway manda como departmentId: precisa ser o ID, não o nome
		"DEBOUNCE_JANELA=" + janelaDebounce,
		"DEBOUNCE_TETO=" + tetoDebounce,
		"BOLHA_PAUSA=" + pausaEntreBolha,
		"RECONCILIA_ATIVO=true",
		"RECONCILIA_INTERVALO=" + intervaloFeed,
		"RECONCILIA_ATRASO=" + atrasoDoCursor,
		"TICKET_FINISH_CONVERSA=false",
		"TICKET_RODAPE=true",
		"TRANSFERIR_APOS_TICKET=true",
		"IDENT_EXIGIR_CPF=false",
		"WEBHOOK_JANELA=5m",
	}
}

func taxonomiaDev(raiz string) string {
	return filepath.Join(raiz, "scripts", "dev", "taxonomia.dev.yaml")
}

// reConfigDoHost: variáveis que o gateway/cérebro leem e que não podem vazar
// do shell do desenvolvedor para a stack do teste.
var reConfigDoHost = regexp.MustCompile(`^(INFODESK_|MOCK_|DEBOUNCE_|RECONCILIA_|TICKET_|IDENT_|BRAIN_|TOOLS_|STRICT_|TAXONOMIA_|DEPARTAMENTO_|ALERTA_|PG_|DB_|WEBHOOK_|PUBLIC_|TURNO_|BOT_|EVENTO_|REPLY_|BOLHA_|TRANSFERIR_|DEDUP_|REFERENCIA_|LOG_LEVEL=|PORT=|GATEWAY_)`)

// iniciar sobe um processo filho; extra vence o ambiente herdado.
func iniciar(nome, dir, exe string, args, extra []string) *proc {
	var env []string
	for _, kv := range os.Environ() {
		if !reConfigDoHost.MatchString(kv) {
			env = append(env, kv)
		}
	}
	env = append(env, extra...)
	l := &bufLog{}
	cmd := exec.CommandContext(context.Background(), exe, args...)
	cmd.Dir, cmd.Env, cmd.Stdout, cmd.Stderr = dir, env, l, l
	p := &proc{nome: nome, cmd: cmd, log: l, fim: make(chan struct{})}
	if err := cmd.Start(); err != nil {
		fmt.Fprintf(l, "falha ao iniciar %s: %v\n", exe, err)
		close(p.fim)
		return p
	}
	go func() { _ = cmd.Wait(); close(p.fim) }()
	return p
}

func (p *proc) matar() {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return
	}
	_ = p.cmd.Process.Kill()
	select {
	case <-p.fim:
	case <-time.After(5 * time.Second):
	}
}

// esperarHTTP espera um GET devolver 2xx; desiste se o processo morrer.
func esperarHTTP(url string, p *proc, limite time.Duration) error {
	cli := &http.Client{Timeout: 2 * time.Second}
	fim := time.Now().Add(limite)
	for time.Now().Before(fim) {
		select {
		case <-p.fim:
			return fmt.Errorf("%s encerrou durante a subida; fim do log:\n%s", p.nome, p.log.cauda(25))
		default:
		}
		req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
		if resp, err := cli.Do(req); err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode/100 == 2 {
				return nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("timeout (%s) esperando %s; fim do log:\n%s", limite, url, p.log.cauda(25))
}

func tresPortas() (a, b, c string, err error) {
	var ls []net.Listener
	defer func() {
		for _, l := range ls {
			l.Close()
		}
	}()
	var out [3]string
	for i := range out {
		l, e := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
		if e != nil {
			return "", "", "", e
		}
		ls = append(ls, l)
		out[i] = strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	}
	return out[0], out[1], out[2], nil
}

func exeSufixo() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// raizDoRepo sobe a partir do diretório do teste até achar backend/go.mod.
func raizDoRepo() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "backend", "go.mod")); err == nil {
			return dir, nil
		}
		pai := filepath.Dir(dir)
		if pai == dir {
			return "", errors.New("não achei a raiz do repositório (backend/go.mod) acima do diretório do teste")
		}
		dir = pai
	}
}

// acharPython devolve um Python 3.12+ que roda de verdade (no Windows o alias
// da Store existe e falha). E2E_PYTHON vence; depois o venv do agente-ia e o PATH.
func acharPython(raiz string) (string, error) {
	var cands []string
	if p := os.Getenv("E2E_PYTHON"); p != "" {
		cands = append(cands, p)
	}
	for _, v := range []string{"venv", ".venv"} {
		cands = append(cands,
			filepath.Join(raiz, "agente-ia", v, "bin", "python"),
			filepath.Join(raiz, "agente-ia", v, "Scripts", "python.exe"))
	}
	cands = append(cands, "python3", "python")
	if la := os.Getenv("LOCALAPPDATA"); la != "" {
		cands = append(cands, filepath.Join(la, "Python", "bin", "python.exe"))
	}
	for _, c := range cands {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := exec.CommandContext(ctx, c, "-c", "import sys; sys.exit(0 if sys.version_info >= (3, 12) else 1)").Run() //nolint:gosec // G702: o interpretador vem de E2E_PYTHON/PATH, escolha de quem roda o teste
		cancel()
		if err == nil {
			return c, nil
		}
	}
	return "", errors.New("nenhum Python 3.12+ funcional (defina E2E_PYTHON ou crie agente-ia/venv)")
}

// idDoDepartamento resolve o ID do departamento no mock (GET /departments):
// DEPARTAMENTO_HUMANO_PADRAO vai no handoff como `departmentId`, então é o ID
// e não o nome — o que um deploy real também precisa fazer.
func idDoDepartamento(mockURL, nome string) (string, error) {
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, mockURL+"/api/integration/v1/departments", nil)
	req.Header.Set("Authorization", "Bearer "+chaveMock)
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return "", fmt.Errorf("GET /departments no mock: %w", err)
	}
	defer resp.Body.Close()
	var deptos []struct{ ID, Name string }
	if err := json.NewDecoder(resp.Body).Decode(&deptos); err != nil {
		return "", fmt.Errorf("GET /departments no mock: resposta ilegível: %w", err)
	}
	for _, d := range deptos {
		if d.Name == nome {
			return d.ID, nil
		}
	}
	return "", fmt.Errorf("o mock não tem o departamento %q (dados.json mudou?)", nome)
}

// caudaUtil é a cauda sem o ruído de 1 linha por segundo do reconciliador
// ("evento duplicado ignorado" dos eventos que o webhook já entregou), que
// empurraria para fora do dump o que explica a falha.
func (l *bufLog) caudaUtil(n int) string {
	var linhas []string
	for _, ln := range strings.Split(strings.TrimRight(l.String(), "\n"), "\n") {
		if strings.Contains(ln, `"delivery":"reconciliador"`) && strings.Contains(ln, "evento duplicado ignorado") {
			continue
		}
		linhas = append(linhas, ln)
	}
	if len(linhas) > n {
		linhas = linhas[len(linhas)-n:]
	}
	return strings.Join(linhas, "\n")
}
