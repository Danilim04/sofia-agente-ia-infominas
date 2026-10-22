# mypy: disallow-untyped-defs, disallow-incomplete-defs, check-untyped-defs, disallow-any-generics, warn-return-any, strict-equality
"""Executa um cenário do juiz contra o grafo REAL com o LLM injetado (real, à noite).

Mesmo ambiente dos cenários determinísticos (`evals/conversa/runner.py`): `build_graph`
com `get_default_tools()`, JEV scriptado (`FakeJEV` com a resposta do turno), retriever
do turno e um `httpx.post` falso que devolve os JSON de `contratos/tools-infodesk/`.
A diferença: o LLM da Sofia não é scriptado — quem responde é o `llm` recebido.

Nada aqui avalia; só registra a `Transcricao` (o que o cliente disse, o que a Sofia
respondeu, que ferramentas rodaram) para o juiz. Falha de um turno (LLM fora do ar,
exceção do grafo) vira `erro` na transcrição — o cenário é contado como erro de
execução e nunca recebe nota do juiz.
"""

from __future__ import annotations

from dataclasses import dataclass, field
from typing import Any, Final

from evals.conversa.runner import (
    _ambiente,
    _colher_turno,
    _contato_da_sessao,
    _GatewayFalso,
    _Relogio,
    _resposta_jev,
    _Retriever,
    dividir_bolhas,
    verificar_persona,
)
from evals.juiz.cenarios import CenarioJuiz

#: Teto de caracteres do resultado das ferramentas mostrado ao juiz.
MAX_SAIDA_FERRAMENTA: Final[int] = 1200


@dataclass
class TurnoTranscrito:
    cliente: str
    resposta: str = ""
    bolhas: list[str] = field(default_factory=list)
    executadas: list[str] = field(default_factory=list)
    recusadas: list[str] = field(default_factory=list)
    saidas_ferramentas: str = ""
    base_conhecimento: list[str] = field(default_factory=list)
    transferiu: bool = False
    bloqueado: bool = False
    #: Violações de persona achadas pelo detector determinístico do runner (contexto extra).
    falhas_persona: list[str] = field(default_factory=list)
    erro: str = ""


@dataclass
class Transcricao:
    cenario_id: str
    turnos: list[TurnoTranscrito] = field(default_factory=list)

    @property
    def erro(self) -> str:
        return next((t.erro for t in self.turnos if t.erro), "")

    @property
    def falhas_persona(self) -> list[str]:
        return [f for t in self.turnos for f in t.falhas_persona]


def executar_com_llm(cenario: CenarioJuiz, llm: Any) -> Transcricao:
    """Roda os turnos do cenário com `llm` e devolve a transcrição (para no 1º turno com erro)."""
    from langchain_core.messages import HumanMessage

    from src.agent.graph import build_graph, get_default_tools
    from tests.fakes import FakeJEV, servico_fake

    base = cenario.base
    jev = FakeJEV()
    gateway = _GatewayFalso()
    relogio = _Relogio(base.extras.relogio)
    retriever = _Retriever()
    thread = f"conv-{base.id}"
    config = {"configurable": {"thread_id": thread}}
    transcricao = Transcricao(base.id)
    with _ambiente(gateway, relogio):
        grafo = build_graph(
            llm=llm,
            tools=get_default_tools(),
            buscar_chunks=retriever,
            classificador=servico_fake(jev),
        )
        for i, turno in enumerate(base.turnos, start=1):
            extras = base.extras.turnos[i - 1]
            relogio.avancar(extras.avanca_minutos)
            retriever.chunks = extras.rag
            gateway.sobrescritas = extras.gateway
            jev._padrao = _resposta_jev(base.jev[i - 1])
            entrada: dict[str, Any] = {"messages": [HumanMessage(content=turno.cliente)]}
            if i == 1:
                entrada.update(
                    telefone=base.sessao.telefone,
                    conversation_id=thread,
                    nome_contato=base.sessao.nome_contato,
                    contato=_contato_da_sessao(base.sessao.contato),
                )
            registro = TurnoTranscrito(
                cliente=turno.cliente,
                base_conhecimento=[str(c.get("texto", "")) for c in extras.rag],
            )
            transcricao.turnos.append(registro)
            try:
                saida = grafo.invoke(entrada, config=config)
            except Exception as exc:
                # só o tipo: a mensagem pode trazer o que o provedor devolveu
                registro.erro = f"{type(exc).__name__} no turno {i}"
                break
            colhido = _colher_turno(saida)
            registro.resposta = colhido.texto
            registro.bolhas = dividir_bolhas(colhido.texto)
            registro.executadas = list(colhido.executadas)
            registro.recusadas = list(colhido.recusadas)
            registro.saidas_ferramentas = colhido.tool_textos.strip()[:MAX_SAIDA_FERRAMENTA]
            registro.bloqueado = colhido.bloqueado
            registro.transferiu = any(a.get("tipo") == "transferir" for a in colhido.acoes) or (
                "transferir_para_atendente" in colhido.executadas
            )
            registro.falhas_persona = verificar_persona(colhido.texto, f"turno {i}")
    return transcricao
