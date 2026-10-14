// Package identity implementa o gate de identidade no edge: resolve quem está
// falando (telefone → base própria → pedir login → Mongo → confirmar um dado)
// ANTES de qualquer chamada ao cérebro. O agente só roda em sessão autenticada.
package identity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"time"

	"bot-azapfy/internal/mongo"
	"bot-azapfy/internal/store"
)

// UserRepo é a dependência de lookup no Mongo (satisfeita por *mongo.Repo).
type UserRepo interface {
	BuscarPorLogin(ctx context.Context, login string) (mongo.UsuarioDoc, bool, error)
}

// UserRepoLote é opcional: quando o repo também busca vários logins de uma
// vez (`$in`), o gate resolve os candidatos de uma frase numa única consulta
// com prazo curto (lookupCandidatosTimeout) em vez de N idas ao Mongo.
type UserRepoLote interface {
	BuscarPorLogins(ctx context.Context, logins []string) ([]mongo.UsuarioDoc, error)
}

// Falhas técnicas (Mongo fora, extrator fora) seguidas toleradas antes de
// transferir ao atendente — a 1ª vira "tente de novo", a 2ª transfere (ZC-4).
const maxFalhasTecnicas = 2

// lookupCandidatosTimeout limita a consulta `$in` dos candidatos de login.
const lookupCandidatosTimeout = 3 * time.Second

// carenciaRevalidacao: com o cache telefone→perfil vencido e o Mongo fora, a
// conversa segue com o perfil antigo por este prazo (cache renovado) em vez
// de travar o cliente num laço de "tente de novo".
const carenciaRevalidacao = time.Hour

// OrigemMongo marca, no Resultado de rotear humano, que a causa foi técnica
// (Mongo), não o cliente — a engine registra a falha com essa origem.
const OrigemMongo = "mongo"

// OrigemCliente marca a transferência pedida pelo PRÓPRIO cliente (palavra de
// escape "atendente"/"humano", ZC-8).
const OrigemCliente = "cliente"

// errExtractorIndisponivel: o cérebro (extrator de login) falhou — é falha
// TÉCNICA (conta em FalhasTecnicas), nunca erro do cliente (ZC-8).
var errExtractorIndisponivel = errors.New("extrator de login indisponível")

// LoginExtractor extrai o identificador (login) de uma mensagem em linguagem
// natural — fallback quando a normalização determinística não casa nenhum
// usuário (ex.: "meu login é joao", "pode usar o email joao@x.com").
// Satisfeito pelo cérebro Python (LLM com saída JSON estruturada).
//
// Importante (anti-injection): o valor devolvido é apenas um CANDIDATO. A
// autorização continua sendo o lookup no Mongo + a confirmação de um dado
// (email/nome). O LLM nunca concede acesso; no pior caso devolve um login que
// não existe ou que falha na confirmação.
type LoginExtractor interface {
	ExtrairLogin(ctx context.Context, mensagem string) (string, error)
}

// Acao é a decisão do gate para o orquestrador (engine) executar no Chatwoot.
type Acao string

const (
	AcaoPerguntar    Acao = "perguntar"     // enviar Reply e aguardar a próxima mensagem
	AcaoSaudar       Acao = "saudar"        // recém-identificado: enviar Reply (saudação), NÃO encaminhar
	AcaoEncaminhar   Acao = "encaminhar"    // identidade pronta: encaminhar a mensagem ao cérebro (Perfil != nil)
	AcaoRotearHumano Acao = "rotear_humano" // desistiu da identificação: enviar Reply e rotear p/ humano
	AcaoIgnorar      Acao = "ignorar"       // nada a fazer (ex.: já roteado p/ humano)
)

// Resultado é a saída de Process.
type Resultado struct {
	Acao   Acao
	Reply  string        // mensagem ao usuário (perguntar/saudar/rotear)
	Perfil *mongo.Perfil // preenchido em AcaoEncaminhar
	Motivo string        // em AcaoRotearHumano: causa em linguagem simples (nota privada/log)
	Origem string        // em AcaoRotearHumano: "" = gate (cliente); OrigemMongo = falha técnica
}

// gateData é o JSON de trabalho persistido em store.GateState.Data. ConfirmValue
// é transitório: some quando a identidade é confirmada (não vai para o cache).
type gateData struct {
	Login          string        `json:"login,omitempty"`
	Tentativas     int           `json:"tentativas,omitempty"`      // erros acumulados no episódio (login + confirmação)
	FalhasTecnicas int           `json:"falhas_tecnicas,omitempty"` // falhas técnicas SEGUIDAS (Mongo/extrator) — não são erro do cliente
	Perfil         *mongo.Perfil `json:"perfil,omitempty"`
	ConfirmField   string        `json:"confirm_field,omitempty"`
	ConfirmValue   string        `json:"confirm_value,omitempty"`
}

// nomeAssistente é o nome com que o bot se apresenta nas saudações.
// Trocar aqui muda o nome em todas as mensagens.
const nomeAssistente = "Zapin"

// Mensagens do gate — persona "Azapfy Suporte" (extraída dos atendimentos
// reais): "Boníssimo dia" + 🧡, "por gentileza", "aqui" = do nosso lado.
// Manter alinhado com a persona em agente-ia/src/agent/prompts.py; emojis
// permitidos: 🧡 ✨ 😉 😊 👍 🙏 (máx. um por mensagem, nunca emoji de riso).
const (
	msgLoginNaoEncontrado    = "Hmm, não encontrei esse login aqui. Consegue conferir o dado e me enviar de novo, por gentileza? 🙏"
	msgFalhaLogin            = "Não consegui localizar o seu login aqui. Vou te encaminhar para um atendente da equipe continuar o atendimento, tudo bem? 🙏"
	msgSemAcesso             = "Verifiquei aqui e o seu acesso está inativo no sistema. Vou te encaminhar para um atendente da equipe resolver isso com você, tudo bem? 🙏"
	msgErroTemporario        = "Opa, tive um problema aqui no sistema e não consegui consultar agora. Consegue tentar de novo em instantes, por gentileza? 🙏"
	msgConfirmacaoNaoConfere = "Esse dado não bateu com o que tenho aqui no cadastro. Consegue conferir e me enviar de novo, por gentileza?"
	msgFalhaConfirmacao      = "Não consegui confirmar a sua identidade por aqui, mas fica tranquilo: vou te transferir para um atendente da equipe continuar com você. 🙏"
	msgSemDadoConfirmacao    = "Para a sua segurança, eu confirmo a identidade por um dado do cadastro — e o seu cadastro não tem esse dado por aqui. Vou te encaminhar para um atendente da equipe continuar o atendimento, tudo bem? 🙏"
	msgFalhaTecnica          = "Estou com uma instabilidade aqui no sistema e não consegui consultar o seu cadastro. Vou te encaminhar para um atendente da equipe continuar o atendimento, tudo bem? 🙏"
	msgPedidoAtendente       = "Claro! Vou te encaminhar para um de nossos atendentes, só um instante. 🙏"
)

// motivoPedidoAtendente é a causa (nota privada/log) da transferência pedida
// pelo cliente.
const motivoPedidoAtendente = "cliente pediu para falar com um atendente"

// Motivos das transferências por falha técnica (nota privada/log).
const (
	motivoMongoFora        = "falha técnica ao consultar o cadastro (2 vezes seguidas)"
	motivoCadastroIlegivel = "cadastro do cliente em formato inesperado (não foi possível ler)"
)

// Motivos (linguagem simples) que acompanham AcaoRotearHumano — vão para a
// nota privada da conversa e para o log `zapin_falha` da engine.
const motivoSemAcesso = "cadastro sem empresa ativa (acesso inativo no sistema)"

func (g *Gate) motivoTentativas(fase string) string {
	return "cliente não conseguiu se identificar (" + fase + " errada " +
		strconv.Itoa(g.maxTentativas) + " vezes)"
}

// saudacaoAbertura devolve a saudação da casa conforme o período do dia em
// Brasília: "Boníssimo dia" / "Boníssima tarde" / "Boa noite" (não existe
// "boníssima noite" no repertório). O cérebro Python tem o equivalente em
// `_periodo_do_dia` (nodes.py) — manter os cortes de horário alinhados.
func saudacaoAbertura(agora time.Time) string {
	if loc, err := time.LoadLocation("America/Sao_Paulo"); err == nil {
		agora = agora.In(loc)
	}
	switch h := agora.Hour(); {
	case h >= 5 && h < 12:
		return "Boníssimo dia"
	case h >= 12 && h < 18:
		return "Boníssima tarde"
	default:
		return "Boa noite"
	}
}

// msgPedirLogin é função (não const) porque a saudação depende do horário.
func msgPedirLogin() string {
	return saudacaoAbertura(time.Now()) + "! Tudo bem por aí? 🧡 Eu sou o " + nomeAssistente +
		", atendente virtual do Suporte Azapfy. Para eu te atender direitinho, me passa o seu login no sistema, por gentileza?"
}

// Gate resolve a identidade por conversa.
type Gate struct {
	store         store.Store
	repo          UserRepo
	extractor     LoginExtractor // opcional: fallback de extração de login via IA
	confirmField  string         // "email" (default) | "nome"
	maxTentativas int
	ttl           time.Duration
	falhaTTL      time.Duration // expiração do estado GateFalha (F1); <=0 → 1h
	log           *slog.Logger
}

// New constrói o gate. `extractor` é opcional (nil = sem fallback de IA: só a
// normalização determinística resolve o login). `falhaTTL` evita o estado
// terminal do F1: passado esse tempo, a conversa em GateFalha volta a ser
// atendida (a identificação recomeça).
func New(st store.Store, repo UserRepo, confirmField string, maxTentativas int, ttl, falhaTTL time.Duration, extractor LoginExtractor, log *slog.Logger) *Gate {
	if log == nil {
		log = slog.Default()
	}
	if maxTentativas <= 0 {
		maxTentativas = 3
	}
	if confirmField != "nome" {
		confirmField = "email"
	}
	if falhaTTL <= 0 {
		falhaTTL = time.Hour
	}
	return &Gate{store: st, repo: repo, extractor: extractor, confirmField: confirmField, maxTentativas: maxTentativas, ttl: ttl, falhaTTL: falhaTTL, log: log}
}

// Process avança a máquina de estados do gate para uma mensagem do usuário.
func (g *Gate) Process(ctx context.Context, convID int64, phone, mensagem string) Resultado {
	gs, err := g.store.GetGate(ctx, convID)
	if err != nil {
		g.log.Error("getGate", "conversation_id", convID, "err", err)
	}
	state := store.GateNovo
	var gd gateData
	if gs != nil {
		state = gs.State
		gd = decodeGate(gs.Data)
	}

	// Palavra de escape (ZC-8): "atendente"/"humano" em QUALQUER fase (novo,
	// login, confirmação, identificado) transfere na hora. Conversa já
	// transferida (GateFalha) segue o tratamento normal abaixo.
	if state != store.GateFalha && PedeAtendente(mensagem) {
		g.salvarGate(ctx, convID, store.GateFalha, gateData{})
		g.log.Info("cliente pediu atendente; transferindo", "conversation_id", convID, "estado_anterior", state)
		return Resultado{Acao: AcaoRotearHumano, Reply: msgPedidoAtendente, Motivo: motivoPedidoAtendente, Origem: OrigemCliente}
	}

	switch state {
	case store.GateIdentificado:
		if gd.Perfil == nil {
			return g.iniciar(ctx, convID, phone)
		}
		// Sem telefone não há cache telefone→perfil a manter: segue com o
		// perfil da conversa (a API de tools exige telefone de todo jeito).
		if phone == "" {
			return Resultado{Acao: AcaoEncaminhar, Perfil: gd.Perfil}
		}
		if p := g.cacheHit(ctx, phone); p != nil {
			return Resultado{Acao: AcaoEncaminhar, Perfil: p}
		}
		// Cache expirado numa conversa já identificada: revalida na ORIGEM
		// (Mongo) pelo login conhecido, sem incomodar o cliente. Mantém o
		// invariante que a API de tools assume — se o gate encaminhou, a
		// identidade em `identities` está viva — e dá propósito ao TTL:
		// perfil desatualizado/acesso revogado é pego aqui.
		return g.revalidar(ctx, convID, phone, gd.Perfil)
	case store.GateFalha:
		// F1: GateFalha não é mais terminal — expirado o TTL, o cliente volta
		// a ser atendido (cache primeiro; senão, recomeça a identificação).
		if gs != nil && time.Since(gs.UpdatedAt) >= g.falhaTTL {
			g.log.Info("gate falha expirado; reiniciando identificação",
				"conversation_id", convID, "desde", gs.UpdatedAt)
			if p := g.cacheHit(ctx, phone); p != nil {
				g.salvarGate(ctx, convID, store.GateIdentificado, gateData{Perfil: p})
				return Resultado{Acao: AcaoEncaminhar, Perfil: p}
			}
			return g.iniciar(ctx, convID, phone)
		}
		return Resultado{Acao: AcaoIgnorar}
	case store.GateAguardLogin:
		return g.tratarLogin(ctx, convID, phone, mensagem, gd)
	case store.GateAguardConfirm:
		return g.tratarConfirmacao(ctx, convID, phone, mensagem, gd)
	default: // novo
		if p := g.cacheHit(ctx, phone); p != nil {
			g.salvarGate(ctx, convID, store.GateIdentificado, gateData{Perfil: p})
			g.log.Info("identidade via cache (base própria)", "conversation_id", convID, "login", mascararLogin(p.Login))
			return Resultado{Acao: AcaoEncaminhar, Perfil: p}
		}
		return g.iniciar(ctx, convID, phone)
	}
}

// cacheHit devolve o perfil em cache para o telefone, se válido.
func (g *Gate) cacheHit(ctx context.Context, phone string) *mongo.Perfil {
	if phone == "" {
		return nil
	}
	ci, err := g.store.GetIdentity(ctx, phone)
	if err != nil {
		g.log.Error("getIdentity", "err", err)
		return nil
	}
	if ci == nil {
		return nil
	}
	var p mongo.Perfil
	if json.Unmarshal([]byte(ci.Perfil), &p) != nil || !p.Encontrado {
		return nil
	}
	return &p
}

func (g *Gate) iniciar(ctx context.Context, convID int64, phone string) Resultado {
	g.salvarGate(ctx, convID, store.GateAguardLogin, gateData{})
	return Resultado{Acao: AcaoPerguntar, Reply: msgPedirLogin()}
}

// revalidar recarrega o perfil na origem quando o cache telefone→perfil expirou
// numa conversa já identificada. Sem interação com o cliente: achou → renova o
// cache e encaminha; sumiu da base → recomeça a identificação; sem empresa
// ativa → mesmo tratamento do tratarLogin (rotear humano); origem fora →
// segue com o perfil antigo por carência (cache renovado por
// carenciaRevalidacao — o cliente já estava identificado; um Mongo instável
// não pode travá-lo num laço de "tente de novo"); cadastro ilegível →
// transfere ao atendente.
func (g *Gate) revalidar(ctx context.Context, convID int64, phone string, antigo *mongo.Perfil) Resultado {
	login := strings.TrimSpace(antigo.Login)
	if login == "" {
		return g.iniciar(ctx, convID, phone)
	}
	doc, found, err := g.repo.BuscarPorLogin(ctx, login)
	if errors.Is(err, mongo.ErrDocumentoInvalido) {
		g.log.Error("revalidacao: cadastro ilegível; transferindo", "conversation_id", convID, "err", err)
		g.salvarGate(ctx, convID, store.GateFalha, gateData{})
		return Resultado{Acao: AcaoRotearHumano, Reply: msgFalhaTecnica, Motivo: motivoCadastroIlegivel, Origem: OrigemMongo}
	}
	if err != nil {
		g.log.Warn("revalidacao: origem fora; seguindo com o perfil antigo por carência",
			"conversation_id", convID, "carencia", carenciaRevalidacao.String(), "err", err)
		g.renovarCachePor(ctx, phone, antigo, carenciaRevalidacao)
		return Resultado{Acao: AcaoEncaminhar, Perfil: antigo}
	}
	if !found {
		g.log.Info("revalidacao: login não existe mais na base; reiniciando identificação", "conversation_id", convID)
		return g.iniciar(ctx, convID, phone)
	}
	perfil := mongo.Projetar(doc)
	if !perfil.TemEmpresaAtiva() {
		g.salvarGate(ctx, convID, store.GateFalha, gateData{})
		g.log.Info("revalidacao: login sem empresa ativa", "conversation_id", convID)
		return Resultado{Acao: AcaoRotearHumano, Reply: msgSemAcesso, Motivo: motivoSemAcesso}
	}
	g.renovarCache(ctx, phone, &perfil)
	g.salvarGate(ctx, convID, store.GateIdentificado, gateData{Perfil: &perfil})
	g.log.Info("identidade revalidada na origem", "conversation_id", convID)
	return Resultado{Acao: AcaoEncaminhar, Perfil: &perfil}
}

func (g *Gate) tratarLogin(ctx context.Context, convID int64, phone, mensagem string, gd gateData) Resultado {
	if strings.TrimSpace(mensagem) == "" {
		return Resultado{Acao: AcaoPerguntar, Reply: msgPedirLogin()}
	}

	doc, login, found, err := g.resolverLogin(ctx, convID, mensagem)
	if errors.Is(err, mongo.ErrDocumentoInvalido) {
		// Existe, mas não dá para ler: repetir não resolve — transfere na hora.
		g.log.Error("buscarPorLogin: cadastro ilegível; transferindo", "conversation_id", convID, "err", err)
		g.salvarGate(ctx, convID, store.GateFalha, gateData{})
		return Resultado{Acao: AcaoRotearHumano, Reply: msgFalhaTecnica, Motivo: motivoCadastroIlegivel, Origem: OrigemMongo}
	}
	if err != nil {
		// Falha técnica (Mongo/extrator fora) NÃO é erro do cliente: não gasta
		// tentativa. Mas também não vira laço infinito de "tente de novo": na
		// 2ª seguida a conversa vai para o atendente.
		gd.FalhasTecnicas++
		g.log.Error("buscarPorLogin", "conversation_id", convID, "falhas_tecnicas", gd.FalhasTecnicas, "err", err)
		if gd.FalhasTecnicas >= maxFalhasTecnicas {
			g.salvarGate(ctx, convID, store.GateFalha, gateData{})
			return Resultado{Acao: AcaoRotearHumano, Reply: msgFalhaTecnica, Motivo: motivoMongoFora, Origem: OrigemMongo}
		}
		g.salvarGate(ctx, convID, store.GateAguardLogin, gd)
		return Resultado{Acao: AcaoPerguntar, Reply: msgErroTemporario}
	}
	gd.FalhasTecnicas = 0 // a origem respondeu: a sequência de falhas técnicas zera
	if !found {
		gd.Tentativas++
		if gd.Tentativas >= g.maxTentativas {
			g.salvarGate(ctx, convID, store.GateFalha, gateData{})
			return Resultado{Acao: AcaoRotearHumano, Reply: msgFalhaLogin, Motivo: g.motivoTentativas("login")}
		}
		g.salvarGate(ctx, convID, store.GateAguardLogin, gd)
		return Resultado{Acao: AcaoPerguntar, Reply: msgLoginNaoEncontrado}
	}

	perfil := mongo.Projetar(doc)
	if !perfil.TemEmpresaAtiva() {
		g.salvarGate(ctx, convID, store.GateFalha, gateData{})
		g.log.Info("login sem empresa ativa", "conversation_id", convID, "login", mascararLogin(login))
		return Resultado{Acao: AcaoRotearHumano, Reply: msgSemAcesso, Motivo: motivoSemAcesso}
	}

	expected, pergunta := g.alvoConfirmacao(doc)
	if expected == "" {
		// ZG-1: sem dado verificável no cadastro não existe segunda verificação —
		// identificar só pelo login deixava quem soubesse o login de outra pessoa
		// ver os chamados dela (conversas #28, #59, #82). Saída segura: fila
		// humana, sem gravar nada no cache telefone→perfil. (Esses cadastros
		// também não conseguem abrir chamado — ZA-2 recusa sem e-mail.)
		g.salvarGate(ctx, convID, store.GateFalha, gateData{})
		g.log.Info("cadastro sem dado de confirmação; roteando para humano",
			"conversation_id", convID, "confirm_field", g.confirmField)
		return Resultado{Acao: AcaoRotearHumano, Reply: msgSemDadoConfirmacao}
	}
	// O contador de erros é do EPISÓDIO de identificação, não da fase: os
	// erros de login gastos até aqui continuam valendo na confirmação, para que
	// o teto (MAX_TENTATIVAS) seja o total de erros antes da fila humana.
	ngd := gateData{
		Login:        login,
		Perfil:       &perfil,
		ConfirmField: g.confirmField,
		ConfirmValue: normalizar(expected),
		Tentativas:   gd.Tentativas,
	}
	g.salvarGate(ctx, convID, store.GateAguardConfirm, ngd)
	g.log.Info("login resolvido, pedindo confirmação", "conversation_id", convID, "login", mascararLogin(login), "confirm_field", g.confirmField)
	return Resultado{Acao: AcaoPerguntar, Reply: pergunta}
}

func (g *Gate) tratarConfirmacao(ctx context.Context, convID int64, phone, mensagem string, gd gateData) Resultado {
	if confereConfirmacao(mensagem, gd.ConfirmValue) {
		return g.identificar(ctx, convID, phone, gd.Perfil)
	}
	gd.Tentativas++
	if gd.Tentativas >= g.maxTentativas {
		g.salvarGate(ctx, convID, store.GateFalha, gateData{})
		return Resultado{Acao: AcaoRotearHumano, Reply: msgFalhaConfirmacao, Motivo: g.motivoTentativas("confirmação")}
	}
	g.salvarGate(ctx, convID, store.GateAguardConfirm, gd)
	return Resultado{Acao: AcaoPerguntar, Reply: msgConfirmacaoNaoConfere}
}

// identificar grava o perfil na base própria (cache c/ TTL), marca o gate como
// identificado e devolve a saudação. O dado de confirmação NÃO é persistido.
func (g *Gate) identificar(ctx context.Context, convID int64, phone string, perfil *mongo.Perfil) Resultado {
	g.renovarCache(ctx, phone, perfil)
	g.salvarGate(ctx, convID, store.GateIdentificado, gateData{Perfil: perfil})
	g.log.Info("identidade confirmada", "conversation_id", convID, "login", mascararLogin(perfil.Login))
	return Resultado{Acao: AcaoSaudar, Reply: saudacao(perfil)}
}

// renovarCache grava/renova o perfil no cache telefone→perfil com novo TTL.
func (g *Gate) renovarCache(ctx context.Context, phone string, perfil *mongo.Perfil) {
	g.renovarCachePor(ctx, phone, perfil, g.ttl)
}

// renovarCachePor é renovarCache com validade explícita (carência da
// revalidação: o perfil não pôde ser conferido na origem, então não ganha o
// TTL cheio — só o prazo de carência, independente do IDENTITY_TTL).
func (g *Gate) renovarCachePor(ctx context.Context, phone string, perfil *mongo.Perfil, ttl time.Duration) {
	if phone == "" || perfil == nil {
		return
	}
	b, err := json.Marshal(perfil)
	if err != nil {
		return
	}
	_ = g.store.PutIdentity(ctx, &store.CachedIdentity{
		Phone:     phone,
		Login:     perfil.Login,
		Perfil:    string(b),
		ExpiresAt: time.Now().Add(ttl),
	})
}

// alvoConfirmacao devolve o valor esperado e a pergunta, conforme ConfirmField.
func (g *Gate) alvoConfirmacao(doc mongo.UsuarioDoc) (valor, pergunta string) {
	if g.confirmField == "nome" {
		return doc.Nome, "Para confirmar que é você mesmo, me confirma o seu nome completo do cadastro, por gentileza?"
	}
	return doc.Email, "Só para confirmar que é você mesmo: consegue me confirmar o e-mail cadastrado na sua conta, por gentileza?"
}

func (g *Gate) salvarGate(ctx context.Context, convID int64, state string, gd gateData) {
	b, _ := json.Marshal(gd)
	if err := g.store.SetGate(ctx, &store.GateState{ConversationID: convID, State: state, Data: string(b)}); err != nil {
		g.log.Error("setGate", "conversation_id", convID, "err", err)
	}
}

func decodeGate(s string) gateData {
	var gd gateData
	if s != "" {
		_ = json.Unmarshal([]byte(s), &gd)
	}
	return gd
}

// resolverLogin tenta identificar o usuário a partir da mensagem em duas
// etapas: (1) candidatos DETERMINÍSTICOS — a própria mensagem, sua versão em
// minúsculas e, quando ela é um CPF/CNPJ formatado puro, os dígitos; (2) se
// nada casar e houver extractor, pede à IA o login embutido na frase e tenta os
// candidatos dele. Devolve o doc, o login que casou e found.
//
// O lookup no Mongo é a validação real: candidatos que não existem simplesmente
// não casam (não há risco em tentar vários).
func (g *Gate) resolverLogin(ctx context.Context, convID int64, mensagem string) (mongo.UsuarioDoc, string, bool, error) {
	if doc, login, found, err := g.tentarCandidatos(ctx, mensagem); found || err != nil {
		return doc, login, found, err
	}

	if g.extractor == nil {
		return mongo.UsuarioDoc{}, "", false, nil
	}
	extraido, err := g.extractor.ExtrairLogin(ctx, mensagem)
	if err != nil {
		// Falha TÉCNICA do cérebro, não do cliente: sobe como erro para o
		// tratarLogin pedir para repetir SEM gastar tentativa (e transferir na
		// 2ª falha técnica seguida) — antes virava "não encontrado" e queimava
		// uma das 3 tentativas do cliente (ZC-8).
		g.log.Warn("extrator de login indisponível", "conversation_id", convID, "err", err)
		return mongo.UsuarioDoc{}, "", false, fmt.Errorf("%w: %v", errExtractorIndisponivel, err)
	}
	if strings.TrimSpace(extraido) == "" {
		return mongo.UsuarioDoc{}, "", false, nil
	}
	// ZG-2: o valor em claro só em DEBUG; em INFO fica mascarado.
	g.log.Info("login extraído por IA", "conversation_id", convID, "login_extraido", mascararLogin(extraido))
	g.log.Debug("login extraído por IA (valor)", "conversation_id", convID, "login_extraido", extraido)
	return g.tentarCandidatos(ctx, extraido)
}

// tentarCandidatos busca no Mongo os candidatos derivados de `texto`,
// devolvendo o primeiro (na ordem dos candidatos) que existir. Com um repo
// que suporta lote (UserRepoLote), é UMA consulta `$in` com prazo de
// lookupCandidatosTimeout; senão, um lookup por candidato.
func (g *Gate) tentarCandidatos(ctx context.Context, texto string) (mongo.UsuarioDoc, string, bool, error) {
	candidatos := loginCandidatos(texto)
	if len(candidatos) == 0 {
		return mongo.UsuarioDoc{}, "", false, nil
	}
	if lote, ok := g.repo.(UserRepoLote); ok {
		lctx, cancel := context.WithTimeout(ctx, lookupCandidatosTimeout)
		defer cancel()
		docs, err := lote.BuscarPorLogins(lctx, candidatos)
		if err != nil {
			return mongo.UsuarioDoc{}, "", false, err
		}
		porLogin := make(map[string]mongo.UsuarioDoc, len(docs))
		for _, d := range docs {
			porLogin[d.Login] = d
		}
		for _, c := range candidatos {
			if doc, found := porLogin[c]; found {
				return doc, c, true, nil
			}
		}
		return mongo.UsuarioDoc{}, "", false, nil
	}
	for _, c := range candidatos {
		doc, found, err := g.repo.BuscarPorLogin(ctx, c)
		if err != nil {
			return mongo.UsuarioDoc{}, "", false, err
		}
		if found {
			return doc, c, true, nil
		}
	}
	return mongo.UsuarioDoc{}, "", false, nil
}

// reEmailEmTexto acha um e-mail embutido numa frase ("meu email é joao@x.com").
var reEmailEmTexto = regexp.MustCompile(`[A-Za-z0-9._%+\-]+@[A-Za-z0-9.\-]+\.[A-Za-z]{2,}`)

// reDocEmTexto acha sequências de dígitos com separadores (CPF/CNPJ) dentro
// de uma frase; o filtro de 11/14 dígitos vem depois.
var reDocEmTexto = regexp.MustCompile(`\d[\d./\- ]{8,20}\d`)

// loginCandidatos deriva, de um texto, as formas de login a tentar no Mongo:
// o texto cru (trim), sua versão minúscula, um CPF/CNPJ formatado puro, um
// e-mail embutido na frase e sequências de EXATAMENTE 11/14 dígitos embutidas
// (F2: "**Fulano:**\n105.966.936.64" resolve sem cair na IA). Não há risco em
// tentar candidatos a mais: o lookup no Mongo é a validação real, e dígitos
// soltos de frases não formam 11/14 dígitos por acaso.
func loginCandidatos(texto string) []string {
	texto = strings.TrimSpace(texto)
	if texto == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(c string) {
		c = strings.TrimSpace(c)
		if c != "" && !seen[c] {
			seen[c] = true
			out = append(out, c)
		}
	}
	add(texto)
	add(strings.ToLower(texto))
	if d := cpfCnpjDigitos(texto); d != "" {
		add(d)
	}
	if m := reEmailEmTexto.FindString(texto); m != "" {
		add(strings.ToLower(m))
		add(m)
	}
	for _, m := range reDocEmTexto.FindAllString(texto, -1) {
		if d := soDigitos(m); len(d) == 11 || len(d) == 14 {
			add(d)
		}
	}
	return out
}

// cpfCnpjDigitos devolve apenas os dígitos quando `s` é um CPF/CNPJ formatado
// puro (só dígitos e os separadores . - / e espaço). Se houver qualquer letra
// ou outro símbolo (logo, não é um CPF/CNPJ isolado), devolve "" — esse caso
// fica para o extractor de IA, evitando casar dígitos soltos de uma frase.
func cpfCnpjDigitos(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	var digitos strings.Builder
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
			digitos.WriteByte(byte(r))
		case r == '.' || r == '-' || r == '/' || r == ' ':
			// separador aceitável de CPF/CNPJ — ignora
		default:
			return "" // tem letra/outro símbolo → não é CPF/CNPJ puro
		}
	}
	return digitos.String()
}

// mascararLogin esconde o login nos logs de nível INFO (ZG-2 / LGPD): só os 3
// últimos caracteres ficam visíveis — o suficiente para correlacionar linhas
// da mesma conversa, não para recompor um CPF ou e-mail. Mesma convenção de
// `_mascarar` em agente-ia/server.py.
func mascararLogin(login string) string {
	r := []rune(strings.TrimSpace(login))
	if len(r) <= 3 {
		return strings.Repeat("*", len(r))
	}
	return strings.Repeat("*", len(r)-3) + string(r[len(r)-3:])
}

// rePedeAtendente: a palavra de escape precisa vir como palavra inteira.
var rePedeAtendente = regexp.MustCompile(`(?i)(^|[^\p{L}])(atendente|humano|humana)([^\p{L}]|$)`)

// reVerboDePedido: num texto longo, só transfere quando há um pedido de fato
// ("quero falar com", "me passa para", "tem algum...") — "o atendente me disse
// ontem que..." em meio a um relato não é pedido.
var reVerboDePedido = regexp.MustCompile(`(?i)\b(falar|conversar|quero|queria|preciso|chamar|chama|passar|passa|transferir|transfere|encaminhar|encaminha|coloca|colocar|tem|existe|cad[eê]|me d[aá])\b`)

// PedeAtendente detecta o pedido explícito do cliente por um atendente humano
// (palavra de escape, ZC-8). Exportado para a engine/testes de outros lotes.
// Mensagem curta (≤ 60 caracteres) com a palavra basta; mensagem longa exige
// também um verbo de pedido perto.
func PedeAtendente(mensagem string) bool {
	m := strings.TrimSpace(mensagem)
	if m == "" || !rePedeAtendente.MatchString(m) {
		return false
	}
	if len([]rune(m)) <= 60 {
		return true
	}
	return reVerboDePedido.MatchString(m)
}

// normalizar deixa a comparação de confirmação tolerante a caixa e espaços.
func normalizar(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
}

// confereConfirmacao decide se a mensagem confirma o dado esperado (F2).
// Igualdade exata quebrava com qualquer texto ao redor ("meu email é X",
// assinatura de relay) e queimava tentativas de cliente legítimo. Regras:
//   - igualdade normalizada; OU
//   - dado esperado (≥5 chars) CONTIDO na mensagem normalizada; OU
//   - para dado numérico (CPF/CNPJ, ≥8 dígitos), os dígitos esperados contidos
//     nos dígitos da mensagem (tolera pontuação trocada: "105.966.936.64").
func confereConfirmacao(mensagem, esperado string) bool {
	got := normalizar(mensagem)
	if got == "" || esperado == "" {
		return false
	}
	if got == esperado {
		return true
	}
	if len([]rune(esperado)) >= 5 && strings.Contains(got, esperado) {
		return true
	}
	if de := soDigitos(esperado); len(de) >= 8 && strings.Contains(soDigitos(got), de) {
		return true
	}
	return false
}

// soDigitos devolve apenas os dígitos de s.
func soDigitos(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteByte(byte(r))
		}
	}
	return b.String()
}

// saudacao é a resposta pós-confirmação de identidade. A saudação de abertura
// ("Boníssimo dia..." + apresentação) já aconteceu em msgPedirLogin — a persona
// não repete saudação na mesma conversa, então aqui é só o fechamento do gate.
func saudacao(p *mongo.Perfil) string {
	if p != nil && p.Nome != "" {
		return "Maravilha, " + primeiroNome(p.Nome) + "! Tudo certo por aqui 🧡 Me conta: como posso te ajudar hoje?"
	}
	return "Maravilha! Tudo certo por aqui 🧡 Me conta: como posso te ajudar hoje?"
}

// primeiroNome devolve só o primeiro nome — mais caloroso que o nome completo.
func primeiroNome(nome string) string {
	if campos := strings.Fields(nome); len(campos) > 0 {
		return campos[0]
	}
	return nome
}
