package engine

// nomeAssistente é o ÚNICO lugar do gateway onde o nome da persona vive.
// O lado Python tem o equivalente em prompts.py — os dois ficam alinhados
// (ver docs/plano/00-visao-geral.md, "Persona").
const nomeAssistente = "Sofia"

// Mensagens da persona que o gateway manda por conta própria (o cérebro
// escreve o resto). Regras: pt-BR informal, "você", no máximo 1 emoji por
// mensagem, nunca prometer prazo, nunca erro técnico.
const (
	// MsgTransferencia é a mensagem neutra ao cliente quando a conversa vai
	// para um atendente por falha — nunca um erro técnico.
	MsgTransferencia = "Vou te encaminhar para um de nossos atendentes, que segue com você por aqui. 🙏"

	// MsgPedidoAtendente responde ao pedido explícito do cliente por um humano.
	MsgPedidoAtendente = "Claro! Vou te encaminhar para um de nossos atendentes, que segue com você por aqui. 🙏"

	// MsgMidia é o aviso ao cliente que mandou áudio, imagem ou arquivo: a
	// Sofia diz o que não consegue fazer e encaminha — nunca fica em silêncio.
	MsgMidia = "Por aqui eu ainda não consigo ver áudio, imagem ou arquivo. Vou te encaminhar para um de nossos atendentes, que confere o que você enviou. 🙏"

	// MsgSemResposta cobre o cérebro que devolveu um reply vazio.
	MsgSemResposta = "Desculpa, não consegui montar uma resposta agora. Você pode me explicar de outro jeito?"
)

// motivoPedidoAtendente é a causa (registro/log/alerta) da transferência
// pedida pelo cliente.
const motivoPedidoAtendente = "cliente pediu para falar com um atendente"
