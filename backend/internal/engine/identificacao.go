package engine

import (
	"context"
	"errors"
	"strings"
	"time"
	"unicode"

	"bot-infominas/internal/alerta"
	"bot-infominas/internal/brain"
	"bot-infominas/internal/infodesk"
	"bot-infominas/internal/store"
)

// prazoLookup limita o GET /contacts/lookup no caminho do primeiro turno: o
// lookup nunca bloqueia o atendimento (ADR 0004, revisão).
const prazoLookup = 10 * time.Second

// identTTLPadrao vale quando Config.IdentTTL não foi definido (testes).
const identTTLPadrao = 24 * time.Hour

// contatoDoTurno devolve o `contato` do Contrato A para o telefone: o que já
// está em `contatos` (lookup, CPF ou contrato informados, dentro do TTL) ou,
// no PRIMEIRO turno do episódio, o resultado de um lookup novo. nil = o
// telefone nunca se identificou (o cérebro pede CPF/contrato).
func (e *Engine) contatoDoTurno(ctx context.Context, convID, telefone string, primeiroTurno bool) *brain.Contato {
	if telefone == "" {
		return nil
	}
	c, err := e.st.ObterContato(ctx, telefone, e.agora())
	if err != nil {
		e.log.Error("ler contato", "conversation_id", convID, "telefone", mascararTelefone(telefone), "err", err)
		c = nil
	}
	if c == nil && primeiroTurno {
		c = e.identificarPorLookup(ctx, convID, telefone)
	}
	return contatoParaCerebro(c)
}

// identificarPorLookup faz o GET /contacts/lookup do telefone (ADR 0004,
// revisão 2026-10-20) e grava `contatos` com origem=lookup quando há
// exatamente UM solicitante. Zero, vários (IDENT_LOOKUP_AMBIGUO=pedir_cpf),
// erro ou resposta ilegível → nil: o fallback é pedir CPF/contrato, nunca
// bloquear. Com IDENT_EXIGIR_CPF=true o lookup só guarda requester_id e
// cliente_id (forma vazia): o CPF continua obrigatório (TODO(N3): valor final
// do plano é decisão de negócio, E5.3).
func (e *Engine) identificarPorLookup(ctx context.Context, convID, telefone string) *store.Contato {
	lctx, cancel := context.WithTimeout(ctx, prazoLookup)
	defer cancel()
	lk, err := e.tr.Lookup(lctx, telefone)
	if err != nil {
		e.log.Warn("lookup do telefone falhou; seguindo sem identificação",
			"conversation_id", convID, "telefone", mascararTelefone(telefone), "err", err)
		var he *infodesk.HTTPError
		if errors.As(err, &he) && (he.Code == infodesk.CodeForbidden || he.Code == infodesk.CodeUnauthorized) {
			e.vigia.Aviso(alerta.TipoChaveSemEscopo, "Chave do Infodesk sem permissão para o lookup",
				"GET /contacts/lookup recusado ("+he.Code+"): confira os escopos da chave (contacts:read).", convID)
		}
		return nil
	}
	if lk.Erro != nil {
		// Objeto presente mas ilegível ≠ telefone desconhecido: o fallback (CPF)
		// continua seguro, mas o motivo tem de aparecer (aviso do revisor da E1.2).
		e.log.Error("lookup devolveu campo ilegível; tratando como não identificado",
			"conversation_id", convID, "err", lk.Erro)
		e.vigia.Aviso(alerta.TipoLookupIlegivel, "Lookup do Infodesk com campo ilegível",
			"GET /contacts/lookup veio com campo que não pôde ser lido ("+lk.Erro.Error()+"). A Sofia pediu CPF por segurança; confira se o formato da API mudou.", convID)
		return nil
	}
	switch n := len(lk.Requesters); {
	case n == 0:
		e.log.Info("lookup: telefone desconhecido; vai pedir CPF/contrato", "conversation_id", convID)
		return nil
	case n > 1:
		// IDENT_LOOKUP_AMBIGUO=pedir_cpf (único valor suportado): não escolhe um.
		e.log.Info("lookup: mais de um solicitante; vai pedir CPF/contrato",
			"conversation_id", convID, "solicitantes", n)
		return nil
	}
	r := lk.Requesters[0]
	if r.ID == "" {
		e.log.Warn("lookup: solicitante sem id; tratando como não identificado", "conversation_id", convID)
		return nil
	}
	clienteID := r.ClientID
	if clienteID == "" && len(lk.ClientIDs) == 1 {
		clienteID = lk.ClientIDs[0]
	}
	ttl := e.cfg.IdentTTL
	if ttl <= 0 {
		ttl = identTTLPadrao
	}
	c := &store.Contato{
		Telefone: telefone, Origem: store.OrigemLookup,
		RequesterID: r.ID, ClienteID: clienteID, NomeCadastro: r.Name,
		ExpiresAt: e.agora().Add(ttl),
	}
	if !e.cfg.IdentExigirCPF {
		c.Forma, c.ValorMascarado = store.FormaTelefone, abreviarNome(r.Name)
	}
	if err := e.st.SalvarContato(ctx, c); err != nil {
		e.log.Error("gravar contato do lookup", "conversation_id", convID, "err", err)
	}
	e.log.Info("lookup: telefone identificado", "conversation_id", convID,
		"forma", c.Forma, "exige_cpf", e.cfg.IdentExigirCPF)
	return c
}

// contatoParaCerebro monta o `contato` do Contrato A. Forma vazia (lookup
// guardado, mas o CPF ainda é exigido) = não identificado = nil.
func contatoParaCerebro(c *store.Contato) *brain.Contato {
	if c == nil {
		return nil
	}
	switch c.Forma {
	case store.FormaTelefone, store.FormaCPF, store.FormaContrato:
	default:
		return nil
	}
	out := &brain.Contato{Forma: c.Forma, ValorMascarado: c.ValorMascarado}
	if c.ClienteID != "" {
		v := c.ClienteID
		out.ClienteID = &v
	}
	if c.NomeCadastro != "" {
		v := c.NomeCadastro
		out.NomeCadastro = &v
	}
	return out
}

// abreviarNome devolve o primeiro nome e a inicial do último ("Maria Souza" →
// "Maria S."): o mínimo para a nota do atendente e para o cérebro, sem o nome
// completo. Nome vazio → "".
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
