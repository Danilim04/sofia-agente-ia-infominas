"""Catálogo dos eventos de log estruturado do cérebro (E4.3).

Formato: `nome campo=valor campo=valor …` numa linha (a mensagem do `logging`),
nome em `snake_case` como PRIMEIRO token. É o que o `grep` e o agregador de log
(Loki/ELK) casam. O gateway Go escreve JSON (`slog`) com os MESMOS nomes de
evento para o que acontece do lado dele (`ticket_aberto`, `transferencia`,
`sofia_alerta`) — a tabela completa dos dois lados está em
`docs/plano/operacao.md`.

Cada evento do cérebro tem aqui o seu contrato de campos (`EVENTOS`); os testes
(`tests/test_observability_logs.py`) fazem os módulos de verdade emitirem o
evento e conferem nome e campos contra este catálogo. Mudar um campo = mudar
aqui, na tabela de `operacao.md` e no teste.

PII: nenhum campo carrega CPF, telefone ou nome em claro. `telefone` sai
mascarado (`*********789`); texto do cliente nunca entra (só `len_mensagem`).
"""

from __future__ import annotations

import logging
import re
from dataclasses import dataclass


@dataclass(frozen=True)
class EventoSpec:
    nome: str
    #: "cerebro" (este processo) — os do gateway estão em `operacao.md`.
    componente: str
    nivel: str
    campos: tuple[str, ...]
    descricao: str


CHAT_REQUEST = "chat_request"
CLASSIFICACAO = "classificacao"
TOOL_EXEC = "tool_exec"
TRANSFERENCIA = "transferencia"
JEV_INDISPONIVEL = "jev_indisponivel"
SOFIA_METRICA = "sofia_metrica"

EVENTOS: dict[str, EventoSpec] = {
    e.nome: e
    for e in (
        EventoSpec(
            CHAT_REQUEST,
            "cerebro",
            "INFO",
            (
                "conversation_id",
                "canal",
                "telefone",
                "identificado",
                "estado_conversa",
                "len_mensagem",
            ),
            "Um turno do Contrato A chegou (POST /chat). `telefone` mascarado; "
            "`identificado` = o gateway mandou `contato` em cache.",
        ),
        EventoSpec(
            CLASSIFICACAO,
            "cerebro",
            "INFO",
            (
                "intencao",
                "categoria",
                "origem",
                "prioridade",
                "frustracao",
                "jev",
                "esclarecer",
                "acoes",
            ),
            "Resultado do roteador (JEV → fallback LLM). `origem`: jev|llm|"
            "esclarecimento|indefinida; `jev` = o JEV respondeu neste turno; "
            "`acoes` = origens das transferências pedidas.",
        ),
        EventoSpec(
            TOOL_EXEC,
            "cerebro",
            "INFO",
            ("nome", "args"),
            "O agente chamou uma tool. `args` = só os NOMES dos argumentos "
            "propostos pelo modelo (valores só em DEBUG `tool_exec_args`, "
            "mascarados).",
        ),
        EventoSpec(
            TRANSFERENCIA,
            "cerebro",
            "INFO",
            ("conversation_id", "origem"),
            "O cérebro devolveu ao gateway a ação `transferir` "
            "(origem: frustracao|confianca|pedido_cliente). A execução do handoff "
            "é o `transferencia` do gateway (campos em `operacao.md`).",
        ),
        EventoSpec(
            JEV_INDISPONIVEL,
            "cerebro",
            "WARNING",
            ("erro",),
            "O JEV falhou neste turno e o roteador seguiu em fail-soft. `erro` = "
            "só o tipo da exceção. Conta a métrica `jev_indisponivel`.",
        ),
        EventoSpec(
            SOFIA_METRICA,
            "cerebro",
            "INFO",
            ("metrica", "total"),
            "Contador em memória subiu (zera no restart). Hoje: `jev_indisponivel`.",
        ),
    )
}

_CAMPO = re.compile(r"(?:^|\s)([a-z_][a-z0-9_]*)=")


def parse_evento(mensagem: str) -> tuple[str, dict[str, str]]:
    """`"tool_exec nome=x args=['a', 'b']"` → `("tool_exec", {"nome": "x", "args": "['a', 'b']"})`.

    Só para testes e ferramentas de ops: valores com `chave=` dentro não se
    separam (não existe tal campo nos eventos do catálogo).
    """
    nome, _, resto = mensagem.strip().partition(" ")
    achados = list(_CAMPO.finditer(" " + resto))
    campos: dict[str, str] = {}
    texto = " " + resto
    for i, m in enumerate(achados):
        fim = achados[i + 1].start() if i + 1 < len(achados) else len(texto)
        campos[m.group(1)] = texto[m.end() : fim].strip()
    return nome, campos


def registrar(logger: logging.Logger, nome: str, **campos: object) -> None:
    """Emite um evento do catálogo no nível dele; campo fora do contrato é erro de programação."""
    spec = EVENTOS[nome]
    extras = set(campos) - set(spec.campos)
    if extras:
        raise ValueError(f"campos fora do contrato de {nome}: {sorted(extras)}")
    corpo = " ".join(f"{k}={campos[k]}" for k in spec.campos if k in campos)
    logger.log(getattr(logging, spec.nivel), "%s %s", nome, corpo)
