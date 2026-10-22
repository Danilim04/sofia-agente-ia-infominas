package toolsapi

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"bot-infominas/internal/config"
	"bot-infominas/internal/engine"
	"bot-infominas/internal/infodesk"
	"bot-infominas/internal/store"
)

// transporteFake é o canal do Infodesk visto pela engine: grava mensagens e handoffs.
type transporteFake struct {
	mu        sync.Mutex
	mensagens []string
	handoffs  []handoffFeito
}

type handoffFeito struct{ conv, dept, nota string }

func (f *transporteFake) EnviarMensagem(_ context.Context, _, texto string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.mensagens = append(f.mensagens, texto)
	return nil
}

func (f *transporteFake) Handoff(_ context.Context, conv, dept, nota string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handoffs = append(f.handoffs, handoffFeito{conv, dept, nota})
	return nil
}

func (f *transporteFake) LerConversa(context.Context, string) (map[string]any, error) {
	return nil, errors.New("não usado")
}

func (f *transporteFake) Lookup(context.Context, string) (infodesk.Lookup, error) {
	return infodesk.Lookup{}, errors.New("não usado")
}

// comEngineReal liga o Transferidor à engine DE VERDADE (caminho único, ADR 0007):
// aviso ao cliente, estado `transferida` e Handoff com nota montada por código.
func comEngineReal(t *testing.T) (*amb, *transporteFake) {
	t.Helper()
	tr := &transporteFake{}
	var eng *engine.Engine
	a := novoAmb(t, func(d *Deps) {
		eng = engine.New(&config.Config{DepartamentoHumanoPadrao: idDeptAtend}, tr, nil, d.Store, d.Log)
		d.Transferidor = eng
	})
	return a, tr
}

func TestEngineReal_TransferirFazHandoffComNotaPorCodigo(t *testing.T) {
	a, tr := comEngineReal(t)
	a.conversa(convTeste, telTeste)
	a.contato(store.Contato{Telefone: telTeste, Origem: store.OrigemLookup, Forma: store.FormaTelefone, ValorMascarado: "Maria S.", NomeCadastro: "Maria Souza"})
	_, _ = a.st.AtualizarConversa(context.Background(), convTeste, func(c *store.Conversa, _ bool) bool {
		c.ClassificacaoJSON = `{"categoria":"sem_conexao","confianca":0.88,"origem":"jev"}`
		return true
	})

	_, out := a.post("conversa/transferir", map[string]any{"conversation_id": convTeste, "motivo": "pedido_cliente", "categoria": "sem_conexao"})
	if out["status"] != true {
		t.Fatalf("transferir: %+v", out)
	}
	if len(tr.handoffs) != 1 || tr.handoffs[0].dept != idDeptAtend {
		t.Fatalf("um handoff, no departamento padrão: %+v", tr.handoffs)
	}
	nota := tr.handoffs[0].nota
	for _, quer := range []string{"motivo: pedido do cliente", "categoria: sem_conexao (jev 0.88)", "solicitante: Maria S.", "sem chamado"} {
		if !strings.Contains(nota, quer) {
			t.Fatalf("nota por código sem %q: %s", quer, nota)
		}
	}
	if strings.Contains(nota, "Souza") || strings.Contains(nota, "5531") {
		t.Fatalf("PII em claro na nota: %s", nota)
	}
	if len(tr.mensagens) != 1 || tr.mensagens[0] != out["mensagem_ao_cliente"] {
		t.Fatalf("o aviso ao cliente é o texto fixo do gateway: %v", tr.mensagens)
	}
	if c, _ := a.st.ObterConversa(context.Background(), convTeste); c.Estado != store.EstadoTransferida {
		t.Fatalf("estado: %s", c.Estado)
	}
	// Segunda tentativa: a conversa já não é do bot.
	if _, out = a.post("conversa/transferir", map[string]any{"conversation_id": convTeste, "motivo": "pedido_cliente"}); out["motivo"] != "ja_transferida" || len(tr.handoffs) != 1 {
		t.Fatalf("idempotente: %+v / %d handoffs", out, len(tr.handoffs))
	}
}

func TestEngineReal_AbrirTransferePosTicketConfirmandoONumeroUmaVez(t *testing.T) {
	a, tr := comEngineReal(t)
	a.conversa(convTeste, telTeste)
	a.identificadoPorCPF()
	out, _ := a.abrir("sem_conexao")
	if out["status"] != true || out["transferido"] != true {
		t.Fatalf("abrir: %+v", out)
	}
	if len(tr.mensagens) != 1 || tr.mensagens[0] != engine.MsgChamadoAberto(48213) {
		t.Fatalf("o gateway confirma o número do chamado, uma vez, antes do handoff (o cérebro é descartado depois dele): %v", tr.mensagens)
	}
	if len(tr.handoffs) != 1 || !strings.Contains(tr.handoffs[0].nota, "chamado #48213") {
		t.Fatalf("a nota do handoff leva o número do chamado (gravado na conversa ANTES): %+v", tr.handoffs)
	}
}

func TestEngineReal_AbrirIndisponivelAvisaOClienteETransfere(t *testing.T) {
	a, tr := comEngineReal(t)
	a.conversa(convTeste, telTeste)
	a.identificadoPorCPF()
	a.idk.postErr = &infodesk.HTTPError{Status: 503, Code: infodesk.CodeInternalError}
	out, _ := a.abrir("sem_conexao")
	if out["motivo"] != "infodesk_indisponivel" {
		t.Fatalf("abrir: %+v", out)
	}
	if len(tr.mensagens) != 1 || tr.mensagens[0] != MsgFalhaAoAbrir {
		t.Fatalf("aviso do gateway antes do handoff: %v", tr.mensagens)
	}
	if len(tr.handoffs) != 1 || !strings.Contains(tr.handoffs[0].nota, "chamado possivelmente criado, confira no Desk antes de abrir outro") || strings.Contains(tr.handoffs[0].nota, "sem chamado") {
		t.Fatalf("handoff com nota: %+v", tr.handoffs)
	}
	if c, _ := a.st.ObterConversa(context.Background(), convTeste); c.Estado != store.EstadoTransferida || c.TicketNumber != 0 {
		t.Fatalf("conversa: %+v", c)
	}
	if strings.Count(MsgFalhaAoAbrir, "🙏") > 1 {
		t.Fatal("no máximo 1 emoji (persona)")
	}
}
