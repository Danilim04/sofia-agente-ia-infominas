package config

import (
	"strings"
	"testing"
	"time"
)

// segredoLongo tem o tamanho mínimo (SegredoMinimo) exigido fora do modo dev.
const segredoLongo = "0123456789abcdef0123456789abcdef"

// comObrigatorias define o mínimo para Load passar das validações.
func comObrigatorias(t *testing.T) {
	t.Helper()
	t.Setenv("INFODESK_BASE_URL", "https://infodesk.test/api/integration/v1/")
	t.Setenv("INFODESK_API_KEY", "chave-de-teste")
	t.Setenv("INFODESK_WEBHOOK_SECRET", segredoLongo)
	t.Setenv("SOFIA_PRODUCAO_ABERTA", "true")
	t.Setenv("TOOLS_API_TOKEN", "")
	t.Setenv("WEBHOOK_SEM_AUTENTICACAO", "")
}

// TestObrigatoriasAusentes: sem INFODESK_BASE_URL/INFODESK_API_KEY o boot
// falha com a mensagem citando as duas variáveis.
func TestObrigatoriasAusentes(t *testing.T) {
	t.Setenv("INFODESK_BASE_URL", "")
	t.Setenv("INFODESK_API_KEY", "")
	_, err := Load()
	if err == nil {
		t.Fatal("esperava erro sem as obrigatórias")
	}
	for _, v := range []string{"INFODESK_BASE_URL", "INFODESK_API_KEY"} {
		if !strings.Contains(err.Error(), v) {
			t.Fatalf("erro devia citar %s: %v", v, err)
		}
	}
}

// TestDefaultsDoGateway: defaults de docs/plano/01-ferramentas.md e a barra
// final da base URL removida.
func TestDefaultsDoGateway(t *testing.T) {
	comObrigatorias(t)
	for _, v := range []string{"INFODESK_TIMEOUT", "REFERENCIA_CACHE_TTL", "DEPARTAMENTO_HUMANO_PADRAO", "TOOLS_API_TOKEN", "PORT"} {
		t.Setenv(v, "")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if cfg.InfodeskBaseURL != "https://infodesk.test/api/integration/v1" {
		t.Fatalf("base URL devia perder a barra final: %q", cfg.InfodeskBaseURL)
	}
	if cfg.InfodeskTimeout != 15*time.Second || cfg.ReferenciaCacheTTL != 10*time.Minute {
		t.Fatalf("defaults do Infodesk: timeout=%v ttl=%v", cfg.InfodeskTimeout, cfg.ReferenciaCacheTTL)
	}
	if cfg.DepartamentoHumanoPadrao != "" || cfg.ToolsAPIToken != "" {
		t.Fatalf("opcionais deviam ficar vazias: %+v", cfg)
	}
	if cfg.Port != "8080" || cfg.TurnoTeto != 105*time.Second || cfg.BrainTimeout != 80*time.Second || cfg.DebounceJanela != 8*time.Second || cfg.ReplyMaxChars != 900 {
		t.Fatalf("defaults de transporte inesperados: %+v", cfg)
	}
}

// TestOrdemDosPrazosDoTurno (E3.7): TURNO_TETO >= DEBOUNCE_TETO + BRAIN_TIMEOUT
// é checado no boot (defaults: 105s >= 20s + 80s).
func TestOrdemDosPrazosDoTurno(t *testing.T) {
	casos := []struct {
		nome, brain, teto, debounce string
		ok                          bool
	}{
		{"defaults", "", "", "", true},
		{"no limite", "80s", "100s", "", true},
		{"folga", "90s", "130s", "", true},
		{"formato composto", "1m20s", "105s", "", true},
		{"um segundo abaixo", "80s", "99s", "", false},
		{"so o brain sobe", "90s", "", "", false},
		{"so o teto desce", "", "90s", "", false},
		{"debounce maior come a folga", "", "", "30s", false},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			comObrigatorias(t)
			t.Setenv("BRAIN_TIMEOUT", c.brain)
			t.Setenv("TURNO_TETO", c.teto)
			t.Setenv("DEBOUNCE_TETO", c.debounce)
			cfg, err := Load()
			if c.ok && err != nil {
				t.Fatalf("esperava aceitar: %v", err)
			}
			if c.ok && c.nome == "defaults" && (cfg.BrainTimeout != 80*time.Second || cfg.TurnoTeto != 105*time.Second) {
				t.Fatalf("defaults: brain=%v teto=%v", cfg.BrainTimeout, cfg.TurnoTeto)
			}
			if !c.ok {
				if err == nil {
					t.Fatal("esperava erro de configuração")
				}
				for _, v := range []string{"BRAIN_TIMEOUT", "TURNO_TETO", "DEBOUNCE_TETO"} {
					if !strings.Contains(err.Error(), v) {
						t.Fatalf("erro devia citar %s: %v", v, err)
					}
				}
			}
		})
	}
}

// TestDuracaoInvalidaCitaAVariavel: valor malformado não vira 0 em silêncio.
func TestDuracaoInvalidaCitaAVariavel(t *testing.T) {
	comObrigatorias(t)
	t.Setenv("INFODESK_TIMEOUT", "quinze")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "INFODESK_TIMEOUT") {
		t.Fatalf("esperava erro citando INFODESK_TIMEOUT, veio %v", err)
	}
}

// TestValoresLidos: as variáveis novas chegam ao Config.
func TestValoresLidos(t *testing.T) {
	comObrigatorias(t)
	t.Setenv("INFODESK_ORGANIZATION_ID", "org-1")
	t.Setenv("INFODESK_WEBHOOK_SECRET", segredoLongo)
	t.Setenv("DEPARTAMENTO_HUMANO_PADRAO", "dept-9")
	t.Setenv("INFODESK_TIMEOUT", "3s")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if cfg.InfodeskOrganizationID != "org-1" || cfg.InfodeskWebhookSecret != segredoLongo ||
		cfg.DepartamentoHumanoPadrao != "dept-9" || cfg.InfodeskTimeout != 3*time.Second {
		t.Fatalf("valores não lidos: %+v", cfg)
	}
}

// TestIdentificacaoEWebhookDefaults: hipóteses de plano configuráveis (07 N3,
// ADR 0004 revisado) e a janela do webhook.
func TestIdentificacaoEWebhookDefaults(t *testing.T) {
	comObrigatorias(t)
	for _, v := range []string{"IDENT_EXIGIR_CPF", "IDENT_LOOKUP_AMBIGUO", "IDENT_TTL", "WEBHOOK_JANELA", "INFODESK_NOME_CHAVE"} {
		t.Setenv(v, "")
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if cfg.IdentExigirCPF || cfg.IdentLookupAmbiguo != "pedir_cpf" || cfg.IdentTTL != 24*time.Hour ||
		cfg.WebhookJanela != 5*time.Minute || cfg.InfodeskNomeChave != "" {
		t.Fatalf("defaults inesperados: %+v", cfg)
	}
}

func TestIdentificacaoEWebhookLidos(t *testing.T) {
	comObrigatorias(t)
	t.Setenv("IDENT_EXIGIR_CPF", "true")
	t.Setenv("IDENT_TTL", "2h")
	t.Setenv("WEBHOOK_JANELA", "90s")
	t.Setenv("INFODESK_NOME_CHAVE", "Sofia")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if !cfg.IdentExigirCPF || cfg.IdentTTL != 2*time.Hour || cfg.WebhookJanela != 90*time.Second || cfg.InfodeskNomeChave != "Sofia" {
		t.Fatalf("valores não lidos: %+v", cfg)
	}
}

// TestLookupAmbiguoDesconhecidoFalha: valor sem implementação não vira
// comportamento inventado em silêncio.
func TestLookupAmbiguoDesconhecidoFalha(t *testing.T) {
	comObrigatorias(t)
	t.Setenv("IDENT_LOOKUP_AMBIGUO", "primeiro")
	if _, err := Load(); err == nil || !strings.Contains(err.Error(), "IDENT_LOOKUP_AMBIGUO") {
		t.Fatalf("esperava erro citando IDENT_LOOKUP_AMBIGUO, veio %v", err)
	}
}

// variaveisE36 são as variáveis que a E3.6 trouxe para cá; os testes as zeram
// para não herdar o ambiente de quem roda.
var variaveisE36 = []string{
	"RECONCILIA_ATIVO", "RECONCILIA_INTERVALO", "RECONCILIA_RETROATIVO", "RECONCILIA_ATRASO", "RECONCILIA_BACKOFF_MAX",
	"RECONCILIA_SKEW", "BOT_TIMEOUT_HANDOFF", "TICKET_PENDENTE_TTL", "TICKET_FINISH_CONVERSA", "TICKET_RODAPE",
	"TRANSFERIR_APOS_TICKET", "TICKET_DEDUP_JANELA", "DEDUP_CONSULTA_INFODESK", "IDENT_CONTRATO_RE", "IDENT_HASH_SAL",
	"STRICT_TAXONOMIA", "TAXONOMIA_PATH", "TESTADORES_TELEFONES",
}

func limparE36(t *testing.T) {
	t.Helper()
	for _, v := range variaveisE36 {
		t.Setenv(v, "")
	}
}

// TestDefaultsDaE36: reconciliador, tools de ticket e taxonomia nascem com os
// defaults documentados em docs/plano/01 e no .env.example.
func TestDefaultsDaE36(t *testing.T) {
	comObrigatorias(t)
	limparE36(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if !cfg.ReconciliaAtivo || cfg.ReconciliaIntervalo != time.Minute || cfg.ReconciliaRetroativo != time.Hour ||
		cfg.ReconciliaAtraso != 2*time.Minute || cfg.ReconciliaBackoffMax != 5*time.Minute || cfg.ReconciliaSkew != time.Minute ||
		cfg.BotTimeoutHandoff != 5*time.Minute || cfg.TicketPendenteTTL != 10*time.Minute {
		t.Fatalf("defaults do reconciliador: %+v", cfg)
	}
	if cfg.TicketFinishConversa || !cfg.TicketRodape || !cfg.TransferirAposTicket || !cfg.DedupConsultaInfodesk ||
		cfg.TicketDedupJanela != 24*time.Hour || cfg.IdentContratoRE != `^\d{4,12}$` || cfg.IdentHashSal != "" {
		t.Fatalf("defaults das tools de ticket: %+v", cfg)
	}
	if cfg.StrictTaxonomia || cfg.TaxonomiaPath != "" {
		t.Fatalf("defaults da taxonomia: %+v", cfg)
	}
}

func TestValoresDaE36Lidos(t *testing.T) {
	comObrigatorias(t)
	limparE36(t)
	for k, v := range map[string]string{
		"RECONCILIA_ATIVO": "false", "RECONCILIA_INTERVALO": "30s", "RECONCILIA_RETROATIVO": "2h", "RECONCILIA_ATRASO": "90s",
		"RECONCILIA_BACKOFF_MAX": "10m", "RECONCILIA_SKEW": "2m", "BOT_TIMEOUT_HANDOFF": "3m", "TICKET_PENDENTE_TTL": "15m",
		"TICKET_FINISH_CONVERSA": "true", "TICKET_RODAPE": "false", "TRANSFERIR_APOS_TICKET": "off",
		"TICKET_DEDUP_JANELA": "12h", "DEDUP_CONSULTA_INFODESK": "0", "IDENT_CONTRATO_RE": `^C\d{6}$`,
		"IDENT_HASH_SAL": "sal-proprio", "STRICT_TAXONOMIA": "sim", "TAXONOMIA_PATH": " /etc/sofia/taxonomia.yaml ",
	} {
		t.Setenv(k, v)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if cfg.ReconciliaAtivo || cfg.ReconciliaIntervalo != 30*time.Second || cfg.ReconciliaRetroativo != 2*time.Hour ||
		cfg.ReconciliaAtraso != 90*time.Second || cfg.ReconciliaBackoffMax != 10*time.Minute || cfg.ReconciliaSkew != 2*time.Minute ||
		cfg.BotTimeoutHandoff != 3*time.Minute || cfg.TicketPendenteTTL != 15*time.Minute {
		t.Fatalf("reconciliador não lido: %+v", cfg)
	}
	if !cfg.TicketFinishConversa || cfg.TicketRodape || cfg.TransferirAposTicket || cfg.DedupConsultaInfodesk ||
		cfg.TicketDedupJanela != 12*time.Hour || cfg.IdentContratoRE != `^C\d{6}$` || cfg.IdentHashSal != "sal-proprio" {
		t.Fatalf("tools de ticket não lidas: %+v", cfg)
	}
	if !cfg.StrictTaxonomia || cfg.TaxonomiaPath != "/etc/sofia/taxonomia.yaml" {
		t.Fatalf("taxonomia não lida: %+v", cfg)
	}
}

// TestValoresInvalidosDaE36Falham: valor malformado, zero ou negativo derruba o
// boot citando a variável (nada de cair em silêncio no default).
func TestValoresInvalidosDaE36Falham(t *testing.T) {
	casos := []struct{ var_, valor string }{
		{"RECONCILIA_INTERVALO", "um minuto"},
		{"RECONCILIA_INTERVALO", "0s"},
		{"RECONCILIA_RETROATIVO", "-1h"},
		{"RECONCILIA_ATRASO", "0"},
		{"RECONCILIA_BACKOFF_MAX", "xyz"},
		{"RECONCILIA_SKEW", "-5s"},
		{"BOT_TIMEOUT_HANDOFF", "invalido"},
		{"TICKET_PENDENTE_TTL", "0s"},
		{"TICKET_DEDUP_JANELA", "-24h"},
		{"RECONCILIA_ATIVO", "talvez"},
		{"TICKET_FINISH_CONVERSA", "ture"},
		{"TICKET_RODAPE", "2"},
		{"TRANSFERIR_APOS_TICKET", "?"},
		{"DEDUP_CONSULTA_INFODESK", "nunca"},
		{"STRICT_TAXONOMIA", "ligado"},
		{"IDENT_CONTRATO_RE", "(abre"},
	}
	for _, c := range casos {
		t.Run(c.var_+"="+c.valor, func(t *testing.T) {
			comObrigatorias(t)
			limparE36(t)
			t.Setenv(c.var_, c.valor)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), c.var_) {
				t.Fatalf("esperava erro citando %s, veio %v", c.var_, err)
			}
		})
	}
}

// TestAvisoDoSalDoHashVazio: IDENT_HASH_SAL vazio funciona, mas o boot avisa
// que rotacionar o TOOLS_API_TOKEN invalida os hashes — e só quando a API de
// tools está exposta.
func TestAvisoDoSalDoHashVazio(t *testing.T) {
	comObrigatorias(t)
	limparE36(t)

	t.Setenv("TOOLS_API_TOKEN", segredoLongo)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	av := avisosCom(cfg, "IDENT_HASH_SAL")
	if len(av) != 1 || !strings.Contains(av[0], "IDENT_HASH_SAL") || !strings.Contains(av[0], "TOOLS_API_TOKEN") ||
		!strings.Contains(av[0], "invalida") {
		t.Fatalf("avisos = %v", av)
	}

	t.Setenv("IDENT_HASH_SAL", "sal")
	if cfg, _ = Load(); len(avisosCom(cfg, "IDENT_HASH_SAL")) != 0 {
		t.Fatalf("com sal próprio não há aviso: %v", cfg.Avisos())
	}

	t.Setenv("IDENT_HASH_SAL", "")
	t.Setenv("TOOLS_API_TOKEN", "")
	if cfg, _ = Load(); len(avisosCom(cfg, "IDENT_HASH_SAL")) != 0 {
		t.Fatalf("sem a API de tools o sal não importa: %v", cfg.Avisos())
	}
}

// Espaço sobrando no valor (compose, systemd, .env sem trim) não derruba o boot.
func TestDuracoesDaE36ToleramEspacos(t *testing.T) {
	comObrigatorias(t)
	limparE36(t)
	t.Setenv("RECONCILIA_INTERVALO", " 30s ")
	t.Setenv("TICKET_PENDENTE_TTL", "15m\t")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if cfg.ReconciliaIntervalo != 30*time.Second || cfg.TicketPendenteTTL != 15*time.Minute {
		t.Fatalf("valores: %+v", cfg)
	}
}

// As variantes brasileiras do mesmo número têm a mesma chave (como o lookup do
// Infodesk: com/sem +55, com/sem o 9º dígito); números diferentes, não.
func TestChaveTelefoneVariantesBR(t *testing.T) {
	mesmo := []string{
		"+5531990000000", "5531990000000", "31990000000", "+553190000000", "553190000000", "3190000000",
		" (31) 99000-0000 ", "+55 31 99000-0000", "+55 (31) 9000-0000",
	}
	quer, ok := ChaveTelefone(mesmo[0])
	if !ok {
		t.Fatalf("telefone válido recusado")
	}
	for _, s := range mesmo {
		if k, ok := ChaveTelefone(s); !ok || k != quer {
			t.Errorf("%q → %q ok=%v, queria %q", s, k, ok, quer)
		}
	}
	for _, s := range []string{"+5531990000001", "+5511990000000", "+5531980000000", "+14155550123", "3133334444"} {
		if k, ok := ChaveTelefone(s); !ok || k == quer {
			t.Errorf("%q não pode casar com o número base: %q ok=%v", s, k, ok)
		}
	}
	// Fixo (10 dígitos sem 9) e o mesmo com +55 casam entre si.
	a, _ := ChaveTelefone("+553133334444")
	b, _ := ChaveTelefone("3133334444")
	if a != b {
		t.Errorf("fixo com e sem +55: %q ≠ %q", a, b)
	}
	// Estrangeiro compara pelo E.164 inteiro.
	if k, _ := ChaveTelefone("+14155550123"); k != "+14155550123" {
		t.Errorf("estrangeiro: %q", k)
	}
}

func TestChaveTelefoneRecusaLixo(t *testing.T) {
	for _, s := range []string{"", "abc", "+55 31 9900", "12345678901234567", "31990000000x", "++5531990000000", "5531-9900-00a0",
		"+5531990000", "+55311234567", "+55991234567", "031990000000", "+031990000000"} {
		if k, ok := ChaveTelefone(s); ok {
			t.Errorf("%q devia ser recusado, veio %q", s, k)
		}
	}
}

func TestTestadoresDesligadaPorPadrao(t *testing.T) {
	comObrigatorias(t)
	limparE36(t)
	t.Setenv("TESTADORES_TELEFONES", "")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TestadoresAtivo() || len(cfg.Testadores()) != 0 {
		t.Fatalf("lista vazia = desligada: %+v", cfg.TestadoresTelefones)
	}
	for _, a := range cfg.Avisos() {
		if strings.Contains(a, "TESTADORES_TELEFONES LIGADA") {
			t.Fatalf("desligada não avisa: %q", a)
		}
	}
}

func TestTestadoresLidosNormalizadosEComAviso(t *testing.T) {
	comObrigatorias(t)
	limparE36(t)
	t.Setenv("TESTADORES_TELEFONES", "+5531990000000, 31990000000 ,+5511988887777,, ")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.TestadoresAtivo() || len(cfg.TestadoresTelefones) != 3 {
		t.Fatalf("itens: %v", cfg.TestadoresTelefones)
	}
	if n := len(cfg.Testadores()); n != 2 { // as duas primeiras são o mesmo número
		t.Fatalf("únicos = %d, queria 2", n)
	}
	var avisos string
	for _, a := range cfg.Avisos() {
		avisos += a + "\n"
	}
	if !strings.Contains(avisos, "TESTADORES_TELEFONES LIGADA com 2 número(s)") || !strings.Contains(avisos, "DEPARTAMENTO_HUMANO_PADRAO vazio") {
		t.Fatalf("avisos: %q", avisos)
	}
	if strings.Contains(avisos, "990000000") || strings.Contains(avisos, "988887777") {
		t.Fatalf("o aviso não pode listar os números: %q", avisos)
	}
}

// Item que não é telefone derruba o boot e não vaza o valor.
func TestTestadoresInvalidosFalhamNoBoot(t *testing.T) {
	for _, v := range []string{"+5531990000000,maria", "123", "+5531990000000;+5511988887777", "+55319900x0000"} {
		t.Run(v, func(t *testing.T) {
			comObrigatorias(t)
			limparE36(t)
			t.Setenv("TESTADORES_TELEFONES", v)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), "TESTADORES_TELEFONES") {
				t.Fatalf("esperava erro citando TESTADORES_TELEFONES, veio %v", err)
			}
			if strings.Contains(err.Error(), "maria") || strings.Contains(err.Error(), "x0000") || strings.Contains(err.Error(), "5511988887777") {
				t.Fatalf("o erro não pode repetir o valor: %v", err)
			}
		})
	}
}

// Número truncado com +55 não pode virar o de outra pessoa (DDD 55 existe), e
// DDD 55 completo continua válido, com e sem +55.
func TestChaveTelefoneDDD55EFixoNaoColideComCelular(t *testing.T) {
	a, ok1 := ChaveTelefone("+5555991234567")
	b, ok2 := ChaveTelefone("55991234567")
	c, ok3 := ChaveTelefone("5591234567")
	if !ok1 || !ok2 || !ok3 || a != b || a != c {
		t.Fatalf("DDD 55 completo: %q %q %q", a, b, c)
	}
	cel, _ := ChaveTelefone("31 9 3333-4444")
	fixo, _ := ChaveTelefone("31 3333-4444")
	if cel == fixo {
		t.Fatalf("celular 931… e fixo 31… não podem colidir: %q", cel)
	}
}

// Cadastro de solicitante (E5.3, ADR 0009): hipóteses configuráveis.
func TestCadastroDefaultsELidos(t *testing.T) {
	comObrigatorias(t)
	t.Setenv("CADASTRO_SEM_CLIENTE", "")
	t.Setenv("CADASTRO_MAX_TENTATIVAS", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if cfg.CadastroSemCliente != CadastroSemClienteTransferir || cfg.CadastroMaxTentativas != 2 {
		t.Fatalf("defaults inesperados: %q %d", cfg.CadastroSemCliente, cfg.CadastroMaxTentativas)
	}

	t.Setenv("CADASTRO_SEM_CLIENTE", " Oferecer ")
	t.Setenv("CADASTRO_MAX_TENTATIVAS", "3")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if cfg.CadastroSemCliente != CadastroSemClienteOferecer || cfg.CadastroMaxTentativas != 3 {
		t.Fatalf("valores lidos inesperados: %q %d", cfg.CadastroSemCliente, cfg.CadastroMaxTentativas)
	}
}

func TestCadastroInvalidosDerrubamOBoot(t *testing.T) {
	for _, c := range []struct{ env, valor string }{
		{"CADASTRO_SEM_CLIENTE", "criar"},
		{"CADASTRO_MAX_TENTATIVAS", "0"},
		{"CADASTRO_MAX_TENTATIVAS", "dois"},
		{"CADASTRO_MAX_TENTATIVAS", "99"},
	} {
		t.Run(c.env+"="+c.valor, func(t *testing.T) {
			comObrigatorias(t)
			t.Setenv(c.env, c.valor)
			if _, err := Load(); err == nil || !strings.Contains(err.Error(), c.env) {
				t.Fatalf("esperava erro citando %s, veio %v", c.env, err)
			}
		})
	}
}

// avisosCom devolve os avisos de boot que citam `trecho`.
func avisosCom(cfg *Config, trecho string) []string {
	var out []string
	for _, a := range cfg.Avisos() {
		if strings.Contains(a, trecho) {
			out = append(out, a)
		}
	}
	return out
}
