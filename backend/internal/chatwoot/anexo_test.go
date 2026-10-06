package chatwoot

import (
	"encoding/json"
	"testing"
)

// payloadAnexoReal: message_created de áudio de WhatsApp como o Chatwoot emite
// (content nulo, anexo em `attachments`; telefone mascarado).
const payloadAnexoReal = `{
 "event": "message_created", "id": 2210,
 "content": null, "message_type": "incoming", "private": false,
 "source_id": "WAID:3EB0A1B2C3D4E5F6A7B8C9",
 "created_at": "2026-10-02 12:11:05 UTC",
 "attachments": [{"id": 77, "message_id": 2210, "file_type": "audio", "account_id": 1,
   "extension": null, "data_url": "http://chatwoot/rails/active_storage/blobs/redirect/abc/audio.ogg",
   "thumb_url": "", "file_size": 18234, "width": null, "height": null}],
 "sender": {"id": 40, "name": "Cliente Teste", "phone_number": "+5531999990000", "type": "contact"},
 "conversation": {"id": 300, "inbox_id": 5, "labels": ["fila-bot"], "status": "open",
   "meta": {"sender": {"id": 40, "name": "Cliente Teste", "phone_number": "+5531999990000", "type": "contact"}, "assignee": null}},
 "account": {"id": 1, "name": "Omni Route"}
}`

// TestMensagemSoComAnexoEhReconhecida (ZD-2): o anexo é lido do webhook e o
// tipo normalizado; `content` nulo não derruba o parse.
func TestMensagemSoComAnexoEhReconhecida(t *testing.T) {
	var msg MessageCreated
	if err := json.Unmarshal([]byte(payloadAnexoReal), &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Content != "" {
		t.Fatalf("content devia ficar vazio, veio %q", msg.Content)
	}
	if !msg.TemAnexo() || msg.TipoAnexo() != AnexoAudio {
		t.Fatalf("esperava anexo de áudio, veio tem=%v tipo=%q", msg.TemAnexo(), msg.TipoAnexo())
	}
	if msg.Attachments[0].FileSize != 18234 || msg.Attachments[0].DataURL == "" {
		t.Fatalf("campos do anexo não lidos: %+v", msg.Attachments[0])
	}
}

// TestTipoAnexoNormaliza: file_type em maiúsculas/espaços vira minúsculo;
// sem file_type é arquivo genérico; sem anexo é vazio.
func TestTipoAnexoNormaliza(t *testing.T) {
	casos := []struct {
		anexos []Attachment
		quer   string
	}{
		{nil, ""},
		{[]Attachment{{FileType: " Image "}}, AnexoImagem},
		{[]Attachment{{FileType: ""}}, AnexoArquivo},
		{[]Attachment{{FileType: "video"}, {FileType: "image"}}, AnexoVideo},
	}
	for _, c := range casos {
		m := MessageCreated{Attachments: c.anexos}
		if got := m.TipoAnexo(); got != c.quer {
			t.Fatalf("%+v: queria %q, veio %q", c.anexos, c.quer, got)
		}
		if m.TemAnexo() != (len(c.anexos) > 0) {
			t.Fatalf("%+v: TemAnexo inesperado", c.anexos)
		}
	}
}
