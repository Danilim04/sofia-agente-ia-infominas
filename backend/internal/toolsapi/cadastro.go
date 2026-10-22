package toolsapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"bot-infominas/internal/config"
	"bot-infominas/internal/engine"
	"bot-infominas/internal/infodesk"
	"bot-infominas/internal/store"
)

// Cadastro de solicitante pela Sofia (ADR 0009, docs/plano/01 A8).
//
//	POST /tools/infodesk/cadastro/preparar    preparar_cadastro_solicitante
//	POST /tools/infodesk/cadastro/cadastrar   cadastrar_solicitante
//
// Quem decide é o gateway: o LLM entrega nome, empresa e e-mail como o cliente
// escreveu; o código valida, confere que o telefone ainda não tem cadastro,
// busca a empresa (GET /clients?q=) e ESCOLHE o cliente entre os candidatos
// devolvidos. O LLM nunca passa clientId nem telefone. O `cadastrar` só executa
// uma proposta que o `preparar` gravou em `cadastros` (mesma conversa, mesmo
// telefone, até propostaCadastroValidade).

// CadastroAPI é a parte do cliente do Infodesk que o cadastro usa
// (*infodesk.Client a satisfaz). Fica fora de InfodeskAPI para os fakes das
// outras tools não precisarem conhecê-la; sem ela as tools respondem
// `infodesk_indisponivel`.
type CadastroAPI interface {
	Lookup(ctx context.Context, phone string) (infodesk.Lookup, error)
	Clientes(ctx context.Context, q string) ([]infodesk.Cliente, error)
	CriarSolicitante(ctx context.Context, n infodesk.NovoSolicitante) (infodesk.SolicitanteCriado, error)
}

// propostaCadastroValidade casa com PROPOSTA_VALIDADE do cérebro (15 min).
// É var só para o teste de expiração.
var propostaCadastroValidade = 15 * time.Minute

// candidatosMostrados é o teto de nomes devolvidos em `empresa_ambigua`.
const candidatosMostrados = 5

const (
	nomeMax    = 120
	empresaMax = 120
	emailMax   = 254

	prazoCadastro = 20 * time.Second
)

// Textos fixos (iguais aos de contratos/tools-infodesk/cadastro/**/erro-*.json).
const (
	txtCadCampoInvalido    = "nome, empresa ou e-mail inválidos; peça o nome completo (nome e sobrenome), o nome da empresa e, se o cliente quiser informar, um e-mail válido"
	txtCadJaCadastrado     = "este telefone já tem cadastro; não cadastre de novo e peça o CPF ou o número do contrato para identificar o cliente"
	txtCadNaoEncontrada    = "não encontrei essa empresa no cadastro; peça para o cliente conferir o nome da empresa e mandar de novo"
	txtCadAmbigua          = "encontrei mais de uma empresa com esse nome; mostre as opções ao cliente e peça para ele dizer qual é a dele"
	txtCadTransferido      = "não foi possível localizar a empresa e a conversa já foi passada para um atendente; não responda mais"
	txtCadEscopo           = "não foi possível fazer o cadastro por aqui; ofereça um atendente"
	txtCadIndisponivel     = "não consegui consultar o cadastro agora; peça desculpa e ofereça um atendente"
	txtCadSemProposta      = "não há cadastro preparado; prepare o cadastro e peça a confirmação do cliente antes"
	txtCadFalhouAoCadastro = "não consegui concluir o cadastro agora; peça desculpa e ofereça um atendente"
	// Com CADASTRO_SEM_CLIENTE=oferecer, depois das tentativas: o LLM não insiste.
	txtCadDesistir = "já tentamos mais de uma vez e não foi possível localizar a empresa; não peça de novo, ofereça um atendente"

	// MsgCadastroSemEmpresa é o aviso do GATEWAY ao cliente quando a empresa não
	// foi localizada e a conversa vai para um atendente (sem prazo, sem emoji).
	MsgCadastroSemEmpresa = "Não consegui localizar a sua empresa no nosso cadastro. Vou te passar para um atendente, que segue com você por aqui."
)

var reEmail = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

// validarNome: duas palavras ou mais, só letras (com acento) e `'`, `-`, `.`.
// Devolve o nome limpo (espaços colapsados) e se é válido.
func validarNome(s string) (string, bool) {
	nome, truncou := sanitizarTexto(s, nomeMax, true)
	if truncou || nome == "" {
		return "", false
	}
	palavras := strings.Fields(nome)
	if len(palavras) < 2 {
		return "", false
	}
	for _, p := range palavras {
		letras := 0
		for _, r := range p {
			switch {
			case unicode.IsLetter(r):
				letras++
			case r == '\'' || r == '’' || r == '-' || r == '.':
			default:
				return "", false
			}
		}
		if letras == 0 {
			return "", false
		}
	}
	return nome, true
}

// validarEmail: vazio é válido (campo opcional); senão regex e teto de tamanho.
func validarEmail(s string) (string, bool) {
	e := strings.TrimSpace(s)
	if e == "" {
		return "", true
	}
	if len(e) > emailMax || !reEmail.MatchString(e) {
		return "", false
	}
	for _, r := range e {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return "", false
		}
	}
	return e, true
}

// empresaMin é o mínimo de letras/dígitos do nome da empresa: impede usar o
// cadastro para listar a carteira de clientes digitando "a", "s", "Silva"...
// (revisão da E5.3). TODO(decisão do produto): valor definitivo.
const empresaMin = 4

// validarEmpresa: uma linha, entre empresaMin letras/dígitos e empresaMax
// caracteres. Texto do cliente — vai à busca (query), nunca é usado como id.
func validarEmpresa(s string) (string, bool) {
	e, truncou := sanitizarTexto(s, empresaMax, true)
	return e, !truncou && len([]rune(strings.ReplaceAll(normalizarEmpresa(e), " ", ""))) >= empresaMin
}

var semAcento = strings.NewReplacer(
	"á", "a", "à", "a", "â", "a", "ã", "a", "ä", "a", "é", "e", "è", "e", "ê", "e", "ë", "e",
	"í", "i", "ì", "i", "î", "i", "ï", "i", "ó", "o", "ò", "o", "ô", "o", "õ", "o", "ö", "o",
	"ú", "u", "ù", "u", "û", "u", "ü", "u", "ç", "c", "ñ", "n",
)

// normalizarEmpresa: minúsculas, sem acento, só letras/dígitos separados por
// um espaço — para comparar o que o cliente digitou com o nome do cadastro.
func normalizarEmpresa(s string) string {
	var b strings.Builder
	espaco := false
	for _, r := range semAcento.Replace(strings.ToLower(s)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if espaco && b.Len() > 0 {
				b.WriteByte(' ')
			}
			espaco = false
			b.WriteRune(r)
		} else {
			espaco = true
		}
	}
	return b.String()
}

// escolherCliente decide, POR CÓDIGO, qual candidato da busca é a empresa do
// cliente: o único de nome idêntico (normalizado) ao digitado ou, sem nome
// idêntico, o único candidato. Mais de um nas duas situações é ambiguidade; o
// segundo retorno traz os candidatos válidos (com id) para listar.
func escolherCliente(cands []infodesk.Cliente, digitado string) (infodesk.Cliente, []infodesk.Cliente, bool) {
	var validos, exatos []infodesk.Cliente
	alvo := normalizarEmpresa(digitado)
	for _, c := range cands {
		if strings.TrimSpace(c.ID) == "" || strings.TrimSpace(c.Nome) == "" {
			continue
		}
		validos = append(validos, c)
		if alvo != "" && normalizarEmpresa(c.Nome) == alvo {
			exatos = append(exatos, c)
		}
	}
	switch {
	case len(exatos) == 1:
		return exatos[0], validos, true
	case len(exatos) == 0 && len(validos) == 1:
		return validos[0], validos, true
	}
	return infodesk.Cliente{}, validos, false
}

// chaveCadastro = sha256(conversation_id + telefone + cliente_id + nome
// normalizado + e-mail em minúsculas) em hex, em 8 grupos de 8 caracteres
// separados por "-". Calculada SÓ pelo gateway; o cérebro apenas a repassa. O
// formato agrupado não é estética: a máscara de CPF do cérebro (src/agent/pii.py)
// troca qualquer corrida de 11 dígitos por "***.***.789-**", o que corromperia ~4%
// das chaves em hex puro. Grupos de 8 nunca têm 9 dígitos seguidos, que a regex exige.
func chaveCadastro(convID, telefone, clienteID, nome, email string) string {
	norm := strings.ToLower(strings.Join(strings.Fields(nome), " "))
	sum := sha256.Sum256([]byte(convID + telefone + clienteID + norm + strings.ToLower(email)))
	h := hex.EncodeToString(sum[:])
	grupos := make([]string, 0, len(h)/8)
	for i := 0; i < len(h); i += 8 {
		grupos = append(grupos, h[i:i+8])
	}
	return strings.Join(grupos, "-")
}

// abreviarNome: "Maria Souza" → "Maria S." (só o nome abreviado sai do gateway).
func abreviarNome(nome string) string {
	partes := strings.Fields(nome)
	switch len(partes) {
	case 0:
		return ""
	case 1:
		return partes[0]
	}
	ultimo := []rune(partes[len(partes)-1])
	if len(ultimo) == 0 || !unicode.IsLetter(ultimo[0]) {
		return partes[0]
	}
	return partes[0] + " " + strings.ToUpper(string(ultimo[0])) + "."
}

type reqCadPreparar struct {
	Telefone       string `json:"telefone"`
	ConversationID string `json:"conversation_id"`
	Nome           string `json:"nome"`
	Empresa        string `json:"empresa"`
	Email          string `json:"email"`
}

func (h *Handler) cadastroAPI() (CadastroAPI, bool) {
	api, ok := h.d.Infodesk.(CadastroAPI)
	return api, ok
}

// recusaCadastroInfodesk traduz o erro do Infodesk no motivo do contrato:
// 401/403 é configuração (escopo contacts:read/write) e vira alerta; o resto é
// indisponibilidade. O texto cru do Infodesk nunca vai ao LLM.
func (h *Handler) recusaCadastroInfodesk(w http.ResponseWriter, conv, onde string, err error, textoIndisponivel string) {
	var he *infodesk.HTTPError
	if errors.As(err, &he) && (he.Code == infodesk.CodeForbidden || he.Code == infodesk.CodeUnauthorized) {
		h.log.Error("cadastro: chave sem escopo", "conversation_id", conv, "onde", onde, "status", he.Status, "code", he.Code)
		h.alertar(AlertaConfiguracao, "Chave do Infodesk sem escopo para o cadastro",
			onde+" recusado ("+he.Code+"): confira os escopos da chave (contacts:read e contacts:write).", conv)
		responderRecusa(w, "escopo_insuficiente", txtCadEscopo)
		return
	}
	if errors.As(err, &he) && he.Status == http.StatusBadRequest {
		h.log.Error("cadastro: Infodesk recusou os dados", "conversation_id", conv, "onde", onde, "code", he.Code)
		h.alertar(AlertaConfiguracao, "Infodesk recusou o cadastro de solicitante",
			onde+" devolveu 400 ("+he.Code+"): campo exigido pela organização ou referência inválida.", conv)
	} else {
		h.log.Warn("cadastro: Infodesk indisponível", "conversation_id", conv, "onde", onde, "err", descreverErro(err))
	}
	responderRecusa(w, "infodesk_indisponivel", textoIndisponivel)
}

// handlePrepararCadastro — preparar_cadastro_solicitante (A8a).
func (h *Handler) handlePrepararCadastro(w http.ResponseWriter, r *http.Request) {
	var req reqCadPreparar
	if !decode(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), prazoCadastro)
	defer cancel()
	conv, tel, ok := h.sessaoEstrita(ctx, req.ConversationID, req.Telefone)
	if !ok {
		responderRecusa(w, "campo_invalido", txtSessaoInvalida)
		return
	}

	nome, okNome := validarNome(req.Nome)
	empresa, okEmpresa := validarEmpresa(req.Empresa)
	email, okEmail := validarEmail(req.Email)
	if !okNome || !okEmpresa || !okEmail {
		h.log.Info("cadastro: campos inválidos", "conversation_id", conv, "nome", okNome, "empresa", okEmpresa, "email", okEmail)
		responderRecusa(w, "campo_invalido", txtCadCampoInvalido)
		return
	}

	// Telefone já ligado a um solicitante nesta conversa/TTL: não cadastra de novo.
	if c := h.contatoIdentificado(ctx, tel); c != nil && c.RequesterID != "" {
		responderRecusa(w, "ja_cadastrado", txtCadJaCadastrado)
		return
	}
	api, ok := h.cadastroAPI()
	if !ok {
		h.log.Error("cadastro: cliente do Infodesk sem Lookup/Clientes/CriarSolicitante")
		responderRecusa(w, "infodesk_indisponivel", txtCadIndisponivel)
		return
	}

	lk, err := api.Lookup(ctx, tel)
	if err != nil {
		h.recusaCadastroInfodesk(w, conv, "GET /contacts/lookup", err, txtCadIndisponivel)
		return
	}
	if lk.Erro != nil {
		// Resposta ilegível ≠ telefone desconhecido: não cadastra às cegas.
		h.log.Error("cadastro: lookup ilegível; não cadastra", "conversation_id", conv, "err", lk.Erro)
		responderRecusa(w, "infodesk_indisponivel", txtCadIndisponivel)
		return
	}
	if len(lk.Requesters) > 0 {
		h.log.Info("cadastro: telefone já tem solicitante no Infodesk", "conversation_id", conv, "solicitantes", len(lk.Requesters))
		responderRecusa(w, "ja_cadastrado", txtCadJaCadastrado)
		return
	}

	cands, err := api.Clientes(ctx, empresa)
	if err != nil {
		h.recusaCadastroInfodesk(w, conv, "GET /clients", err, txtCadIndisponivel)
		return
	}
	cliente, validos, achou := escolherCliente(cands, empresa)
	if !achou {
		h.empresaNaoResolvida(ctx, w, conv, tel, validos)
		return
	}

	linha, err := h.d.Store.GravarCadastroProposta(ctx, &store.Cadastro{
		Chave:          chaveCadastro(conv, tel, cliente.ID, nome, email),
		ConversationID: conv, Telefone: tel, Nome: nome, Email: email,
		ClienteID: cliente.ID, ClienteNome: textoExterno(cliente.Nome, empresaMax),
	})
	if err != nil || linha == nil {
		h.log.Error("cadastro: gravar proposta", "conversation_id", conv, "err", err)
		responderRecusa(w, "infodesk_indisponivel", txtCadIndisponivel)
		return
	}
	if err := h.d.Store.ZerarTentativasCadastro(ctx, h.chaveTentativasCadastro(tel)); err != nil {
		h.log.Warn("cadastro: zerar tentativas", "conversation_id", conv, "err", err)
	}
	h.log.Info("cadastro preparado", "conversation_id", conv, "telefone", mascararTelefone(tel), "candidatos", len(validos))
	writeJSON(w, http.StatusOK, map[string]any{
		"status": true,
		// Sem nome nem e-mail: o modelo já os tem (passou-os no preparo) e o
		// ToolMessage vai para o checkpoint e o trace, que mascaram por chave, não
		// por texto. A empresa é a que o GATEWAY escolheu: o resumo ao cliente usa ela.
		"proposta": map[string]any{
			"empresa":            map[string]any{"nome": linha.ClienteNome},
			"telefone_mascarado": mascararTelefone(tel), "chave": linha.Chave,
		},
	})
}

// empresaNaoResolvida conta a tentativa e responde: pedir de novo, ou — esgotadas
// CADASTRO_MAX_TENTATIVAS — aplicar CADASTRO_SEM_CLIENTE.
func (h *Handler) empresaNaoResolvida(ctx context.Context, w http.ResponseWriter, conv, tel string, validos []infodesk.Cliente) {
	motivo, texto := "empresa_nao_encontrada", txtCadNaoEncontrada
	if len(validos) > 1 {
		motivo, texto = "empresa_ambigua", txtCadAmbigua
	}
	n, err := h.d.Store.IncrementarTentativasCadastro(ctx, h.chaveTentativasCadastro(tel))
	if err != nil {
		h.log.Error("cadastro: contar tentativa", "conversation_id", conv, "err", err)
		n = 1 // sem contador, nunca transfere por engano: pede de novo
	}
	h.log.Info("cadastro: empresa não resolvida", "conversation_id", conv, "motivo", motivo, "tentativa", n, "candidatos", len(validos))

	if n >= h.op.CadastroMaxTentativas {
		// TODO(decisão do produto): o que fazer com empresa inexistente. Hipótese:
		// transferir (CADASTRO_SEM_CLIENTE); nunca cadastra sem clientId.
		if h.op.CadastroSemCliente == config.CadastroSemClienteTransferir && h.d.Transferidor != nil {
			// Contexto próprio (como em abrir.go): se o cérebro desistir de esperar, o
			// handoff ainda precisa terminar de ser registrado.
			tctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), posTeto)
			defer cancel()
			id := h.d.Transferidor.TransferirParaHumano(tctx, conv, engine.Transferencia{
				Origem:   engine.OrigemAcao,
				Causa:    "cadastro: empresa do cliente não encontrada no Infodesk",
				Detalhe:  "CADASTRO_SEM_CLIENTE=transferir após " + strconv.Itoa(n) + " tentativas (" + motivo + ")",
				Mensagem: MsgCadastroSemEmpresa,
			})
			h.log.Info("cadastro transferido", "conversation_id", conv, "id_ocorrencia", id)
			responderRecusa(w, "cadastro_transferido", txtCadTransferido)
			return
		}
		responderRecusa(w, motivo, txtCadDesistir)
		return
	}

	extra := map[string]any{}
	if motivo == "empresa_ambigua" {
		nomes := make([]map[string]any, 0, candidatosMostrados)
		visto := map[string]bool{}
		for _, c := range validos {
			nome := textoExterno(c.Nome, empresaMax)
			if nome == "" || visto[nome] {
				continue
			}
			visto[nome] = true
			nomes = append(nomes, map[string]any{"nome": nome})
			if len(nomes) == candidatosMostrados {
				break
			}
		}
		extra["candidatos"] = nomes
	}
	resp := recusa(motivo, texto)
	for k, v := range extra {
		resp[k] = v
	}
	writeJSON(w, http.StatusOK, resp)
}

type propostaCadIn struct {
	Chave string `json:"chave"`
}

type reqCadastrar struct {
	Telefone       string         `json:"telefone"`
	ConversationID string         `json:"conversation_id"`
	Proposta       *propostaCadIn `json:"proposta"`
}

// handleCadastrar — cadastrar_solicitante (A8b). Executa EXATAMENTE a proposta
// que o `preparar` gravou: nome, e-mail e clientId saem da linha `cadastros`,
// nunca do corpo; o telefone é o da conversa. `POST /requesters` UMA vez.
func (h *Handler) handleCadastrar(w http.ResponseWriter, r *http.Request) {
	var req reqCadastrar
	if !decode(w, r, &req) {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), prazoCadastro)
	defer cancel()
	conv, tel, ok := h.sessaoEstrita(ctx, req.ConversationID, req.Telefone)
	if !ok {
		responderRecusa(w, "sem_proposta", txtCadSemProposta)
		return
	}
	if req.Proposta == nil || strings.TrimSpace(req.Proposta.Chave) == "" {
		responderRecusa(w, "sem_proposta", txtCadSemProposta)
		return
	}
	linha, err := h.d.Store.ObterCadastro(ctx, req.Proposta.Chave)
	if err != nil {
		h.log.Error("cadastrar: ler cadastro", "conversation_id", conv, "err", err)
		responderRecusa(w, "infodesk_indisponivel", txtCadFalhouAoCadastro)
		return
	}
	// Linha `feito` perdeu nome e e-mail (E4.5, I2): a chave não se recalcula, mas
	// ela só chega aqui vinda do cérebro e precisa ser da MESMA conversa e telefone.
	if linha == nil || linha.ConversationID != conv || !mesmoTelefone(linha.Telefone, tel) ||
		(linha.Status != store.CadastroFeito && linha.Chave != chaveCadastro(conv, linha.Telefone, linha.ClienteID, linha.Nome, linha.Email)) {
		h.log.Warn("cadastrar: proposta desconhecida ou de outra conversa", "conversation_id", conv, "existe", linha != nil)
		responderRecusa(w, "sem_proposta", txtCadSemProposta)
		return
	}

	if linha.Status == store.CadastroFeito {
		// Já concluído (retry, ou "sim" repetido): devolve o resultado, sem novo POST.
		h.log.Info("cadastrar: já concluído; devolvendo o resultado guardado", "conversation_id", conv)
		linha.Nome = h.nomeDoContato(ctx, tel) // a linha não guarda mais o nome (I2); o contato, com TTL, sim
		// Se a primeira chamada concluiu no Infodesk mas não gravou o contato, este é o
		// retry que repara (a gravação é idempotente).
		if err := h.salvarContatoCadastrado(ctx, tel, linha); err != nil {
			h.log.Error("cadastrar: gravar contato", "conversation_id", conv, "err", err)
			responderRecusa(w, "infodesk_indisponivel", txtCadFalhouAoCadastro)
			return
		}
		h.respostaCadastro(w, linha)
		return
	}
	if time.Since(linha.CreatedAt) > propostaCadastroValidade {
		responderRecusa(w, "sem_proposta", txtCadSemProposta)
		return
	}

	api, ok := h.cadastroAPI()
	if !ok {
		h.log.Error("cadastrar: cliente do Infodesk sem CriarSolicitante")
		responderRecusa(w, "infodesk_indisponivel", txtCadFalhouAoCadastro)
		return
	}
	// Telefone E.164 da CONVERSA (nunca do LLM); nome/e-mail/clientId da linha gravada.
	criado, err := api.CriarSolicitante(ctx, infodesk.NovoSolicitante{
		Name: linha.Nome, ClientID: linha.ClienteID, Phone: tel, Email: linha.Email, ConversationID: conv,
	})
	if err != nil {
		h.recusaCadastroInfodesk(w, conv, "POST /requesters", err, txtCadFalhouAoCadastro)
		return
	}

	if err := h.d.Store.ConcluirCadastro(ctx, linha.Chave, criado.ID, criado.Created); err != nil {
		// O Infodesk já cadastrou; repetir é seguro (idempotente por telefone/e-mail).
		h.log.Error("cadastrar: concluir cadastro", "conversation_id", conv, "err", err)
		responderRecusa(w, "infodesk_indisponivel", txtCadFalhouAoCadastro)
		return
	}
	linha.Status, linha.RequesterID, linha.Criado = store.CadastroFeito, criado.ID, criado.Created
	if err := h.salvarContatoCadastrado(ctx, tel, linha); err != nil {
		h.log.Error("cadastrar: gravar contato", "conversation_id", conv, "err", err)
		responderRecusa(w, "infodesk_indisponivel", txtCadFalhouAoCadastro)
		return
	}
	h.log.Info("solicitante_cadastrado", "conversation_id", conv, "criado", criado.Created)
	h.respostaCadastro(w, linha)
}

// salvarContatoCadastrado grava a identificação do telefone (origem `cadastro`)
// para o cérebro e as outras tools. Com IDENT_EXIGIR_CPF a forma fica vazia (o
// cadastro não dispensa o CPF); um CPF/contrato já informado é preservado.
func (h *Handler) salvarContatoCadastrado(ctx context.Context, tel string, l *store.Cadastro) error {
	c := &store.Contato{Telefone: tel}
	if antigo, err := h.d.Store.ObterContato(ctx, tel, h.agora()); err != nil {
		return err
	} else if antigo != nil {
		*c = *antigo
	}
	c.RequesterID, c.ClienteID, c.NomeCadastro = l.RequesterID, l.ClienteID, l.Nome
	c.ExpiresAt = h.agora().Add(h.op.IdentTTL)
	if c.Forma != store.FormaCPF && c.Forma != store.FormaContrato {
		c.Origem = store.OrigemCadastro
		c.Forma, c.ValorMascarado = "", ""
		if !h.op.IdentExigirCPF {
			c.Forma, c.ValorMascarado = store.FormaTelefone, abreviarNome(l.Nome)
		}
	}
	return h.d.Store.SalvarContato(ctx, c)
}

func (h *Handler) respostaCadastro(w http.ResponseWriter, l *store.Cadastro) {
	writeJSON(w, http.StatusOK, map[string]any{
		"status": true, "cadastrado": true, "criado": l.Criado,
		"solicitante":  map[string]any{"nome": abreviarNome(l.Nome)},
		"empresa":      map[string]any{"nome": l.ClienteNome},
		"requester_id": l.RequesterID,
	})
}

// chaveTentativasCadastro identifica QUEM tenta cadastrar: o hash do telefone
// (nunca o número em claro na tabela). Contar por conversa deixava um mesmo
// telefone tentar sem fim abrindo conversas novas (E4.5, M5).
func (h *Handler) chaveTentativasCadastro(tel string) string {
	sal := h.op.IdentSal
	if sal == "" {
		sal = h.token
	}
	return "tel:" + hashComSal(sal, "cadastro-tentativas:"+normalizarTelefone(tel))
}

// nomeDoContato devolve o nome do cadastro que ficou em `contatos` (com TTL),
// ou "" se já expirou: a linha `cadastros` concluída não guarda mais o nome.
func (h *Handler) nomeDoContato(ctx context.Context, tel string) string {
	if c, err := h.d.Store.ObterContato(ctx, tel, h.agora()); err == nil && c != nil {
		return c.NomeCadastro
	}
	return ""
}
