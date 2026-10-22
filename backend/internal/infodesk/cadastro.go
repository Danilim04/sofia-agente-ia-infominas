package infodesk

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// Cadastro de solicitante (ADR 0009, docs/infodesk/04 "Contatos e solicitantes"):
// `GET /clients?q=` acha a empresa e `POST /requesters` cadastra a pessoa já
// vinculada à conversa.

// clientesLimite é quantos candidatos a busca pede: o cadastro só decide entre
// "um", "o de nome idêntico" ou "ambíguo", e mostra poucos ao cliente.
const clientesLimite = 20

// Cliente é a empresa de `GET /clients` (04): `{id, name}`; o resto do objeto
// (documento, razão social...) não é usado. Nome vem do cadastro do Infodesk,
// mas continua dado externo (regra 4).
type Cliente struct {
	ID   string `json:"id"`
	Nome string `json:"name"`
}

// NovoSolicitante é o corpo de `POST /requesters`. Name, ClientID e Phone são
// obrigatórios para a Sofia (nunca cadastra sem cliente); Email e
// ConversationID são opcionais, mas com `conversationId` o solicitante já sai
// vinculado à conversa. Name, Phone e Email são PII.
type NovoSolicitante struct {
	Name           string `json:"name"`
	ClientID       string `json:"clientId"`
	Phone          string `json:"phone"`
	Email          string `json:"email,omitempty"`
	ConversationID string `json:"conversationId,omitempty"`
}

// SolicitanteCriado é a resposta `200` de `POST /requesters`: o solicitante e
// o `created` (false = já existia com o mesmo telefone/e-mail no cliente,
// docs/infodesk/10 §6). O objeto completo fica em Bruto.
type SolicitanteCriado struct {
	Solicitante
	Created bool
	Bruto   map[string]any
}

// UnmarshalJSON é tolerante: campo com tipo inesperado fica no zero value.
func (s *SolicitanteCriado) UnmarshalJSON(b []byte) error {
	bruto, campos, err := decodificarObjeto(b)
	if err != nil {
		return err
	}
	*s = SolicitanteCriado{Bruto: bruto}
	lerCampo(campos, "id", &s.ID)
	lerCampo(campos, "name", &s.Name)
	lerCampo(campos, "email", &s.Email)
	lerCampo(campos, "phone", &s.Phone)
	lerCampo(campos, "clientId", &s.ClientID)
	lerCampo(campos, "client", &s.Client)
	lerCampo(campos, "created", &s.Created)
	return nil
}

// Clientes faz `GET /clients?q=` e devolve a primeira página (até
// clientesLimite) de empresas cujo nome/razão social casa com `q`. Escopo
// contacts:read. `q` é texto digitado pelo cliente: vai só na query, nunca em
// erro ou log deste pacote. Aceita array puro ou `{data, nextCursor}` (07 D3).
func (c *Client) Clientes(ctx context.Context, q string) ([]Cliente, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, ErrCampoObrigatorio
	}
	query := url.Values{"q": {q}}
	setLimiteECursor(query, clientesLimite, "")
	p, err := paginaDe[Cliente](ctx, c, "/clients", query)
	if err != nil {
		return nil, err
	}
	return p.Data, nil
}

// CriarSolicitante faz `POST /requesters` UMA vez (nenhum retry: este pacote
// não repete requisições). O Infodesk é idempotente por telefone/e-mail dentro
// do mesmo cliente, então o chamador pode repetir depois de um erro ambíguo,
// mas isso é decisão dele. Escopo contacts:write (403 `FORBIDDEN` sem ele).
// 2xx sem `id` no corpo é ErroResposta: o cadastro pode ter acontecido, mas o
// gateway não tem o que vincular.
func (c *Client) CriarSolicitante(ctx context.Context, n NovoSolicitante) (SolicitanteCriado, error) {
	for _, campo := range []struct{ nome, valor string }{
		{"name", n.Name}, {"clientId", n.ClientID}, {"phone", n.Phone},
	} {
		if strings.TrimSpace(campo.valor) == "" {
			return SolicitanteCriado{}, ErrCampoObrigatorio
		}
	}
	var out SolicitanteCriado
	if err := c.do(ctx, http.MethodPost, "/requesters", nil, n, &out); err != nil {
		return SolicitanteCriado{}, err
	}
	if out.ID == "" {
		return out, &ErroResposta{Rota: "/requesters", Motivo: "resposta sem id do solicitante"}
	}
	return out, nil
}
