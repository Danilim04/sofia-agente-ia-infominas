package engine

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"bot-azapfy/internal/alerta"
	"bot-azapfy/internal/brain"
	"bot-azapfy/internal/chatwoot"
	"bot-azapfy/internal/store"
)

// Origens de uma transferência para a fila humana (campo `origem` da tabela
// `falhas` e do log `zapin_falha`).
const (
	OrigemGate      = "gate"      // identificação falhou (login/confirmação/acesso inativo)
	OrigemCerebro   = "cerebro"   // o cérebro Python falhou (classe transitória/definitiva)
	OrigemMongo     = "mongo"     // cadastro inacessível/ilegível no Mongo
	OrigemDisjuntor = "disjuntor" // disjuntor aberto: o cérebro nem foi chamado
	OrigemCliente   = "cliente"   // o cliente pediu um atendente
	OrigemAnexo     = "anexo"     // o cliente mandou áudio/imagem/arquivo, que o bot não lê (ZD-2)
)

// MsgTransferencia é a mensagem neutra ao cliente quando a conversa vai para
// a fila humana por falha — nunca um erro técnico (mesma voz do gate).
const MsgTransferencia = "Vou te encaminhar para um de nossos atendentes, só um instante. 🙏"

// MsgAnexo é o aviso ao cliente que mandou um anexo (ZD-2): o Zapin diz o que
// não consegue fazer e encaminha — nunca fica em silêncio. Um emoji só.
func MsgAnexo(tipo string) string {
	var oque string
	switch tipo {
	case chatwoot.AnexoAudio:
		oque = "ouvir áudio"
	case chatwoot.AnexoImagem:
		oque = "ver imagem"
	case chatwoot.AnexoVideo:
		oque = "ver vídeo"
	default:
		oque = "abrir arquivo"
	}
	return "Por aqui eu ainda não consigo " + oque + ". Vou te encaminhar para um de nossos atendentes, que vai conferir o que você enviou — só um instante. 🙏"
}

// descreveAnexo nomeia o tipo do anexo para a nota privada/registro.
func descreveAnexo(tipo string) string {
	switch tipo {
	case chatwoot.AnexoAudio:
		return "áudio"
	case chatwoot.AnexoImagem:
		return "imagem"
	case chatwoot.AnexoVideo:
		return "vídeo"
	case "", chatwoot.AnexoArquivo:
		return "arquivo"
	}
	return "anexo (" + tipo + ")"
}

// Transferencia descreve por que a conversa vai para a fila humana.
type Transferencia struct {
	Origem   string // OrigemGate, OrigemCerebro, ... (obrigatório)
	Classe   string // brain.ClasseTransitoria | brain.ClasseDefinitiva | "" (não se aplica)
	Causa    string // linguagem simples: nota privada, alerta, log
	Detalhe  string // técnico, sem dado pessoal: só log/tabela
	ID       string // ID de ocorrência já existente (ex.: vindo do cérebro); vazio = gerar
	Mensagem string // mensagem ao cliente; vazia = MsgTransferencia
}

// transferenciaPorErroDoCerebro monta a transferência a partir do erro da
// chamada ao cérebro: reaproveita classe, causa e ID quando o cérebro os
// mandou (Contrato A), para o mesmo ID aparecer nos dois lados.
func transferenciaPorErroDoCerebro(err error) Transferencia {
	t := Transferencia{Origem: OrigemCerebro, Detalhe: err.Error()}
	if be, ok := brain.ComoErro(err); ok {
		t.Classe, t.Causa, t.ID = be.Classe, be.Causa, be.IDOcorrencia
	}
	if t.Causa == "" {
		t.Causa = "falha ao processar a mensagem no cérebro"
	}
	return t
}

// NovoIDOcorrencia gera `ZPF-AAAAMMDD-HHMM-NN` (horário de Brasília) — o
// MESMO formato que o cérebro Python usa (src/agent/falhas.py), legível na
// nota privada e localizável no log.
func NovoIDOcorrencia(agora time.Time) string {
	if loc, err := time.LoadLocation("America/Sao_Paulo"); err == nil {
		agora = agora.In(loc)
	}
	return fmt.Sprintf("ZPF-%s-%s-%02d", agora.Format("20060102"), agora.Format("1504"), rand.IntN(100)) //nolint:gosec // G404: sufixo legível de ID de ocorrência, não é material criptográfico
}

// TransferirParaHumano é o ÚNICO caminho para a fila humana — usado pelo gate
// (identificação falhou), pelas falhas do cérebro/Mongo, pelo disjuntor e pelo
// pedido explícito do cliente. Faz, nesta ordem e sem interromper nas falhas
// parciais (cada passo loga o próprio erro):
//
//  1. ID de ocorrência (gera se não veio), log estruturado `zapin_falha` e
//     registro na tabela `falhas` — o mesmo ID em todos;
//  2. mensagem neutra ao cliente (nunca erro técnico);
//  3. etiqueta `fila-bot` → `fila-humano`, equipe padrão (TEAM_HUMANO_ID) e
//     status `open` (conversa pendente/adiada volta a aparecer na fila);
//  4. nota privada com data/hora, causa em linguagem simples e o ID.
//
// Devolve o ID de ocorrência. Reutilizável pelos lotes seguintes (ZB/ZD):
// quem precisar transferir monta uma Transferencia e chama aqui.
func (e *Engine) TransferirParaHumano(ctx context.Context, conv *chatwoot.Conversation, t Transferencia) string {
	agora := e.agora()
	id := strings.TrimSpace(t.ID)
	if id == "" {
		id = NovoIDOcorrencia(agora)
	}
	if t.Causa == "" {
		t.Causa = "transferido pelo Zapin"
	}

	e.log.Error("zapin_falha",
		"id_ocorrencia", id, "conversation_id", conv.ID,
		"origem", t.Origem, "classe", t.Classe, "causa", t.Causa, "detalhe", t.Detalhe)
	if err := e.st.RegistrarFalha(ctx, &store.Falha{
		ID: id, ConversationID: conv.ID, Origem: t.Origem, Classe: t.Classe,
		Causa: t.Causa, Detalhe: t.Detalhe, CreatedAt: agora,
	}); err != nil {
		e.log.Error("registrar falha", "id_ocorrencia", id, "conversation_id", conv.ID, "err", err)
	}
	// Alertas (ZC-7): turno do cérebro que terminou em falha conta na taxa;
	// falha definitiva/técnica alerta na hora.
	if t.Origem == OrigemCerebro || t.Origem == OrigemDisjuntor {
		e.vigia.Turno(false)
	}
	e.vigia.FalhaRegistrada(alerta.Falha{
		ID: id, ConversationID: conv.ID, Origem: t.Origem, Classe: t.Classe,
		Causa: t.Causa, Link: e.linkConversa(conv.ID),
	})

	msg := t.Mensagem
	if msg == "" {
		msg = MsgTransferencia
	}
	e.send(ctx, conv.ID, msg)

	if e.cfg.LabelHumano != "" {
		labels := chatwoot.ReplaceLabel(conv.Labels, e.cfg.LabelBot, e.cfg.LabelHumano)
		if err := e.cw.SetLabels(ctx, conv.ID, labels); err != nil {
			e.log.Error("transferir: etiqueta da fila humana", "id_ocorrencia", id, "conversation_id", conv.ID, "err", err)
		}
	}
	if e.cfg.TeamHumanoID > 0 {
		if err := e.cw.AssignTeam(ctx, conv.ID, e.cfg.TeamHumanoID); err != nil {
			e.log.Error("transferir: atribuir equipe", "id_ocorrencia", id, "conversation_id", conv.ID, "team_id", e.cfg.TeamHumanoID, "err", err)
		}
	}
	if conv.Status != chatwoot.StatusOpen {
		if err := e.cw.ToggleStatus(ctx, conv.ID, chatwoot.StatusOpen); err != nil {
			e.log.Error("transferir: reabrir conversa", "id_ocorrencia", id, "conversation_id", conv.ID, "err", err)
		}
	}
	if err := e.cw.SendMessage(ctx, conv.ID, notaDeTransferencia(agora, t, id), true); err != nil {
		e.log.Error("transferir: nota privada", "id_ocorrencia", id, "conversation_id", conv.ID, "err", err)
	}
	e.log.Info("conversa transferida para a fila humana",
		"id_ocorrencia", id, "conversation_id", conv.ID, "origem", t.Origem)
	return id
}

// notaDeTransferencia é a nota privada que o atendente lê ao pegar a conversa.
func notaDeTransferencia(agora time.Time, t Transferencia, id string) string {
	if loc, err := time.LoadLocation("America/Sao_Paulo"); err == nil {
		agora = agora.In(loc)
	}
	var b strings.Builder
	b.WriteString("Zapin transferiu esta conversa para a equipe.\n")
	fmt.Fprintf(&b, "Quando: %s (Brasília)\n", agora.Format("02/01/2006 15:04"))
	fmt.Fprintf(&b, "Motivo: %s\n", t.Causa)
	if t.Classe != "" {
		fmt.Fprintf(&b, "Tipo de falha: %s\n", descreveClasse(t.Classe))
	}
	fmt.Fprintf(&b, "Ocorrência: %s", id)
	return b.String()
}

func descreveClasse(classe string) string {
	switch classe {
	case brain.ClasseTransitoria:
		return "temporária (o sistema tentou de novo antes de transferir)"
	case brain.ClasseDefinitiva:
		return "definitiva (precisa de ação do responsável — já alertado)"
	}
	return classe
}
