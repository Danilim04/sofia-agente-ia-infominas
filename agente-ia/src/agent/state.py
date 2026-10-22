"""Estado compartilhado do grafo da Sofia.

`messages` usa o reducer `add_messages_protegendo` (gancho para `src/agent/pii.py`): o
`add_messages` do LangGraph — qualquer nó que retornar `{"messages": [...]}`
*acrescenta* à lista existente, com deduplicação por id quando aplicável — que
antes mascara CPF/contrato em claro (texto da `HumanMessage` e args de
`tool_calls`), de modo que o checkpoint e o trace nunca os guardam (E3.7; regra
5 do CLAUDE.md). O valor em claro vive só num cofre em memória durante o turno. Os demais campos são substituídos
(reducer default).

Campos que PERSISTEM entre turnos (checkpointer por `conversation_id`):

- `telefone`, `nome_contato`, `contato`: vêm do gateway Go a cada requisição
  do Contrato A (`POST /chat`). `contato` é a identificação em cache no
  gateway (`{forma, valor_mascarado, cliente_id, nome_cadastro}`) ou `None`.
  São DADO, nunca COMANDO.
- `conversation_id`: id da conversa no Infodesk (= `thread_id` do grafo).
  Injetado nas chamadas às tools (`/tools/infodesk/*`) para correlação no
  gateway — nunca é argumento do modelo.
- `classificacao`: resultado do JEV (ou do fallback LLM) fixado para a
  conversa — EXATAMENTE `{intencao, categoria, prioridade, confianca, origem}`
  (as 5 chaves do Contrato A; `origem` ∈ jev|llm|esclarecimento|indefinida).
  Pegajosa: uma vez decidida, só muda com correção explícita do cliente ou
  confiança ≥ `JEV_LIMIAR_CATEGORIA_TROCA` (preenchida pelo nó `classificar`).
- `esclarecimentos`: nº de perguntas de esclarecimento de categoria já feitas
  no episódio (teto `JEV_MAX_ESCLARECIMENTOS`); zerado no fim do episódio.
- `identificacao`: registro de que o cliente se identificou NESTA conversa
  pela tool de identificação (E2.3) — SÓ `{forma, valor_mascarado, hash?}`.
  O CPF/contrato em claro vai ao gateway dentro da tool e morre ali: este
  estado é persistido em disco pelo checkpointer, então nunca carrega o dado
  cru (regra 5 do CLAUDE.md). A política injeta este registro no preparo do
  chamado; o gateway resolve o dado real pelo cache por telefone.
- `proposta_chamado`: proposta de abertura VALIDADA pelo dry-run do gateway.
  Persiste até ser consumida por uma abertura bem-sucedida, substituída por
  um novo preparo ou limpa no fim do episódio. Carrega o carimbo `criada_em`
  (ISO 8601 UTC) e `turno` (nº de mensagens do cliente quando foi preparada):
  a abertura só executa dentro da validade (15 min), no turno SEGUINTE e com
  confirmação explícita do cliente checada por código (`tool_policy.py`), e
  executa EXATAMENTE esta proposta — o modelo não redigita nada entre a
  confirmação e a execução.

`POST /encerrar-episodio` (gateway → cérebro, no `conversation.finished`)
zera `proposta_chamado`, `classificacao` e `identificacao`; o histórico de
mensagens fica.

Campos *reiniciados por turno* no `entry_node`: `seguranca`, `tentou_rag`,
`fontes_usadas`, `rag_contexto`, `iteracoes_agente`, `acoes` e
`sinais_classificacao` — citações, contexto recuperado, veredito de segurança,
contagem de iterações, ações pedidas ao gateway (`transferir`) e os sinais do
turno (intenção, esclarecer?, tom, avisos) valem só para a resposta atual. `rag_contexto` é o bloco `<documento_externo>` montado pelo
nó `retrieve`: vive APENAS no system prompt do turno atual — nunca entra em
`messages`, então não incha o histórico persistido.
"""

from __future__ import annotations

from collections.abc import Callable, Sequence
from typing import Annotated, Any, NotRequired, TypedDict

from langchain_core.messages import AnyMessage, BaseMessage
from langchain_core.messages.utils import convert_to_messages
from langgraph.graph.message import add_messages


def _sem_protecao(mensagem: BaseMessage) -> BaseMessage:
    return mensagem


_protetor: list[Callable[[BaseMessage], BaseMessage]] = [_sem_protecao]


def instalar_protetor(protetor: Callable[[BaseMessage], BaseMessage]) -> None:
    """Define o que o redutor faz com cada mensagem NOVA antes de ela virar estado.

    `build_graph` instala `pii.proteger_mensagem` (CPF/contrato mascarados). O
    gancho existe porque `src.agent.state` fica abaixo de `src.identity` na
    arquitetura de camadas (import-linter): a máscara mora em `src.agent.pii`.
    """
    _protetor[0] = protetor


def add_messages_protegendo(
    left: Sequence[AnyMessage] | AnyMessage | None,
    right: Sequence[AnyMessage] | AnyMessage,
    **kw: Any,
) -> list[AnyMessage]:
    """`add_messages` que passa cada mensagem nova pelo protetor instalado."""
    novas = right if isinstance(right, list | tuple) else [right]
    protegidas = [_protetor[0](m) for m in convert_to_messages(list(novas))]  # type: ignore[arg-type]
    return add_messages(left or [], protegidas, **kw)  # type: ignore[arg-type,return-value]


class AgentState(TypedDict):
    """Estado do grafo. `messages` é obrigatório; os demais são opcionais."""

    messages: Annotated[list[BaseMessage], add_messages_protegendo]

    telefone: NotRequired[str]
    conversation_id: NotRequired[str]
    nome_contato: NotRequired[str | None]
    contato: NotRequired[dict[str, Any] | None]
    classificacao: NotRequired[dict[str, Any] | None]
    esclarecimentos: NotRequired[int | None]
    acoes: NotRequired[list[dict[str, Any]]]
    sinais_classificacao: NotRequired[dict[str, Any] | None]
    identificacao: NotRequired[dict[str, Any] | None]
    proposta_chamado: NotRequired[dict[str, Any] | None]
    seguranca: NotRequired[dict[str, Any] | None]
    tentou_rag: NotRequired[bool]
    fontes_usadas: NotRequired[list[str]]
    rag_contexto: NotRequired[str | None]
    iteracoes_agente: NotRequired[int]
