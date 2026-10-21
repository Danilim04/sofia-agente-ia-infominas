package engine

import (
	"context"
	"errors"

	"bot-infominas/internal/infodesk"
)

// ErrJanelaWhatsAppFechada: o Infodesk recusou o texto livre porque o cliente
// não fala há mais de 24 h (`409 WHATSAPP_WINDOW_CLOSED`). É definitivo —
// a v1 não envia template —, então a engine encerra o episódio local.
var ErrJanelaWhatsAppFechada = errors.New("engine: janela de 24 h do WhatsApp fechada")

// ClienteInfodesk é o que a engine usa do *infodesk.Client.
type ClienteInfodesk interface {
	EnviarMensagem(ctx context.Context, convID, texto string, interna bool) error
	Handoff(ctx context.Context, convID string, h infodesk.Handoff) error
	Conversa(ctx context.Context, id string) (infodesk.Conversa, error)
	Lookup(ctx context.Context, phone string) (infodesk.Lookup, error)
}

type transporteInfodesk struct{ c ClienteInfodesk }

// NovoTransporteInfodesk liga o cliente do Infodesk à interface Transporte.
func NovoTransporteInfodesk(c ClienteInfodesk) Transporte { return transporteInfodesk{c: c} }

func (t transporteInfodesk) EnviarMensagem(ctx context.Context, convID, texto string) error {
	err := t.c.EnviarMensagem(ctx, convID, texto, false)
	var he *infodesk.HTTPError
	if errors.As(err, &he) && he.Code == infodesk.CodeWhatsAppWindowClosed {
		return errors.Join(ErrJanelaWhatsAppFechada, err)
	}
	return err
}

func (t transporteInfodesk) Handoff(ctx context.Context, convID, deptID, note string) error {
	h := infodesk.Handoff{Note: note}
	if deptID != "" {
		h.DepartmentID = &deptID
	}
	return t.c.Handoff(ctx, convID, h)
}

func (t transporteInfodesk) LerConversa(ctx context.Context, convID string) (map[string]any, error) {
	c, err := t.c.Conversa(ctx, convID)
	if err != nil {
		return nil, err
	}
	return c.Bruto, nil
}

func (t transporteInfodesk) Lookup(ctx context.Context, telefone string) (infodesk.Lookup, error) {
	return t.c.Lookup(ctx, telefone)
}
