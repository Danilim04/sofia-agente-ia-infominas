"""Construção do StateGraph da Sofia.

`build_graph()` aceita injeção opcional de `llm`, `tools`, `checkpointer` e
`buscar_chunks` para facilitar testes — em produção o default é o LLM do
OpenRouter (via `get_llm()`), as tools canônicas, `MemorySaver` (o `server.py`
injeta o checkpointer SQLite persistente) e o retriever real da base.

Fluxo (retrieval-first; E2.2 insere o nó `classificar` entre o guardrail e o
`retrieve` — ver `nodes.py`):

    entry → input_guardrail → (malicioso) → safe_response → END
                              (demais)   → retrieve → agent ⇄ tools
                                                        ↓
                                              output_guardrail → END

Uso:

    graph = build_graph()
    out = graph.invoke(
        {"telefone": "11999990001", "messages": [HumanMessage("oi")]},
        config={"configurable": {"thread_id": "11999990001"}},
    )
"""

from __future__ import annotations

from collections.abc import Callable
from typing import Any

from langchain_core.tools import BaseTool
from langgraph.checkpoint.memory import MemorySaver
from langgraph.graph import END, StateGraph

from src.agent.llm import get_llm
from src.agent.nodes import (
    entry_node,
    input_guardrail_node,
    make_agent_node,
    make_retrieve_node,
    make_tools_node,
    output_guardrail_node,
    route_after_agent,
    route_after_input_guardrail,
    safe_response_node,
)
from src.agent.state import AgentState


def get_default_tools() -> list[BaseTool]:
    """Tools canônicas expostas ao modelo.

    VAZIA nesta etapa (E1.3): as tools do domínio antigo saíram e as 6 tools
    do Infodesk (`docs/plano/01-ferramentas.md`, seção A) entram em E2.3 —
    junto com as entradas correspondentes em `tool_policy.POLITICAS` e os
    efeitos em `src/tools/efeitos.py`.

    Invariantes que esta lista sempre respeita: o agente não acessa a
    internet; a base de conhecimento NÃO é tool (o retrieval é etapa fixa do
    grafo — nó `retrieve` — para o modelo não decidir "se" consulta);
    identidade/escopo nunca são argumentos do LLM (injetados e validados pela
    política); nenhum mock/harness entra aqui (`tests/test_default_tools.py`).
    """
    return []


def build_graph(
    llm: Any | None = None,
    tools: list[BaseTool] | None = None,
    checkpointer: Any | None = None,
    buscar_chunks: Callable[[str], dict] | None = None,
):
    """Compila o grafo. Defaults de produção podem ser sobrescritos para testes."""
    if llm is None:
        llm = get_llm()
    if tools is None:
        tools = get_default_tools()
    if checkpointer is None:
        checkpointer = MemorySaver()

    workflow: StateGraph = StateGraph(AgentState)

    workflow.add_node("entry", entry_node)
    workflow.add_node("input_guardrail", input_guardrail_node)
    workflow.add_node("retrieve", make_retrieve_node(buscar_chunks))
    workflow.add_node("agent", make_agent_node(llm, tools))
    workflow.add_node("tools", make_tools_node(tools))
    workflow.add_node("output_guardrail", output_guardrail_node)
    workflow.add_node("safe_response", safe_response_node)

    workflow.set_entry_point("entry")
    workflow.add_edge("entry", "input_guardrail")
    workflow.add_conditional_edges(
        "input_guardrail",
        route_after_input_guardrail,
        {"retrieve": "retrieve", "safe_response": "safe_response"},
    )
    workflow.add_edge("retrieve", "agent")
    workflow.add_conditional_edges(
        "agent",
        route_after_agent,
        {"tools": "tools", "output_guardrail": "output_guardrail"},
    )
    workflow.add_edge("tools", "agent")
    workflow.add_edge("output_guardrail", END)
    workflow.add_edge("safe_response", END)

    return workflow.compile(checkpointer=checkpointer)
