"""Estado compartilhado do grafo do agente (Épico 7).

`messages` usa o reducer `add_messages` do LangGraph — qualquer nó que
retornar `{"messages": [...]}` *acrescenta* à lista existente, com
deduplicação por id quando aplicável. Os demais campos são substituídos
(reducer default).

`telefone` e `identidade` persistem entre turnos (via `MemorySaver`).
A `identidade` é o perfil mínimo resolvido pelo gate (telefone → base própria
→ login → Mongo Azapfy) e transportado no Contrato A: empresas/bases com
acesso, módulos ativos e o tipo de permissão (`grupo_user`). É DADO, nunca
COMANDO.
`seguranca`, `tentou_rag`, `fontes_usadas`, `rag_contexto` e
`iteracoes_agente` são *reiniciados por turno* no `entry_node` — citações,
contexto recuperado, veredito de segurança e contagem de iterações valem só
para a resposta atual.

`rag_contexto` é o bloco `<documento_externo>` montado pelo nó `retrieve`
(retrieval-first): vive APENAS no system prompt do turno atual — nunca entra
em `messages`, então não incha o histórico persistido.

`conversation_id` é o id da conversa no Chatwoot (= `thread_id` do grafo),
vindo da requisição do Contrato A. Persiste entre turnos e é INJETADO nas
chamadas às tools do SAC (`/tools/sac/*`) só para correlação nos logs do
gateway — nunca é argumento do modelo.

`proposta_chamado` é a proposta de abertura VALIDADA pelo dry-run do gateway
(`/tools/sac/preparar`): persiste entre turnos (a confirmação do cliente vem
no turno seguinte) até ser consumida por uma abertura bem-sucedida,
substituída por um novo preparo ou limpa quando a conversa é RESOLVIDA no
Chatwoot (o gateway chama `POST /encerrar-episodio` — a conversa é reutilizada
quando o contato volta a escrever, e uma proposta antiga não pode sobreviver
ao episódio). Ela carrega o carimbo `criada_em` (ISO 8601 UTC) e `turno`
(nº de mensagens do cliente quando foi preparada): `abrir_chamado_suporte`
só executa dentro da validade (15 min), no turno SEGUINTE e com confirmação
explícita do cliente checada por código (`tool_policy.py`, ZB-1). Executa
EXATAMENTE esta proposta — o modelo não redigita nada entre a confirmação e a
execução.
"""

from __future__ import annotations

from typing import Annotated, Any, NotRequired, TypedDict

from langchain_core.messages import BaseMessage
from langgraph.graph.message import add_messages


class AgentState(TypedDict):
    """Estado do grafo. `messages` é obrigatório; os demais são opcionais."""

    messages: Annotated[list[BaseMessage], add_messages]

    telefone: NotRequired[str]
    conversation_id: NotRequired[str]
    identidade: NotRequired[dict[str, Any] | None]
    proposta_chamado: NotRequired[dict[str, Any] | None]
    seguranca: NotRequired[dict[str, Any] | None]
    tentou_rag: NotRequired[bool]
    fontes_usadas: NotRequired[list[str]]
    rag_contexto: NotRequired[str | None]
    iteracoes_agente: NotRequired[int]