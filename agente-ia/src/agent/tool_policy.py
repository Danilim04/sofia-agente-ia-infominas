"""Política de autorização das tools — "o LLM propõe, o código dispõe".

Princípio herdado (F7/F9): o LLM NUNCA faz parte da base confiável do
sistema. Toda tool call que ele emite é tratada como input não-confiável —
como se tivesse vindo do próprio cliente — e passa por esta política antes de
executar. Identidade e escopo vêm SEMPRE do estado da sessão (telefone do
webhook, classificação do JEV, identificação validada por código, proposta
carimbada pelo dry-run), nunca de argumento escolhido pelo modelo.

Este módulo é a fonte única de verdade sobre o que o agente pode fazer:

- ``injetar``: args preenchidos pelo ``tools_node`` a partir do ESTADO da
  sessão. Nas tools eles são ``InjectedToolArg`` — ficam FORA do schema que o
  modelo vê, então não existe sequência de tokens capaz de controlá-los.
  Qualquer valor que o modelo tente passar nesses nomes é sobrescrito.
- ``validar``: args que o LLM fornece legitimamente (ex.: um CPF digitado
  pelo cliente) mas que só passam se forem válidos/compatíveis com a sessão.
  Defesa em profundidade — o gateway Go revalida server-side.
- ``exigir``: precondição de SESSÃO (ex.: proposta preparada, confirmada e
  dentro da validade antes de abrir chamado).

Uma tool ausente deste mapa não recebe nem valida nada da sessão (tools puras
de conteúdo). Auditoria de segurança das tools começa e termina neste arquivo.

Estado desta etapa (E1.3): o MECANISMO fica (``PoliticaTool``,
``aplicar_politica``, ``carimbar_proposta``, ``_exigir_proposta_confirmada``,
``CONFIRMACOES_EXPLICITAS``); as ENTRADAS do domínio antigo saíram e
``POLITICAS`` nasce vazio — E2.3 preenche com as 6 tools do Infodesk
(`docs/plano/01-ferramentas.md`, seção D).
"""

from __future__ import annotations

import re
import unicodedata
from collections.abc import Callable, Iterable, Mapping
from dataclasses import dataclass, field
from datetime import UTC, datetime, timedelta
from typing import Any

from langchain_core.messages import BaseMessage, HumanMessage

Resolver = Callable[[Mapping[str, Any]], Any]
Validador = Callable[[Any, Mapping[str, Any]], str | None]
Precondicao = Callable[[Mapping[str, Any]], str | None]


@dataclass(frozen=True)
class PoliticaTool:
    """Política de uma tool: o que injetar da sessão e o que validar do LLM.

    ``exigir`` é uma precondição de SESSÃO (não de argumento): se devolver um
    erro, a tool não executa — independente do que o modelo mandou. É o que
    torna o fluxo em duas fases determinístico (ex.: abrir chamado exige
    proposta preparada no estado).
    """

    injetar: dict[str, Resolver] = field(default_factory=dict)
    validar: dict[str, Validador] = field(default_factory=dict)
    exigir: Precondicao | None = None


# ---------------------------------------------------------------------------
# Resolvers — derivam valores do estado da sessão (nunca do LLM)
# ---------------------------------------------------------------------------


def _telefone_da_sessao(state: Mapping[str, Any]) -> str:
    """Telefone do cliente (vem do webhook do Infodesk via Contrato A)."""
    return str(state.get("telefone") or "")


def _conversation_id_da_sessao(state: Mapping[str, Any]) -> str:
    """Id da conversa no Infodesk (Contrato A) — correlação no gateway."""
    return str(state.get("conversation_id") or "")


def _proposta_da_sessao(state: Mapping[str, Any]) -> dict[str, Any] | None:
    """Proposta de chamado validada pelo dry-run do gateway (estado do grafo)."""
    proposta = state.get("proposta_chamado")
    return dict(proposta) if isinstance(proposta, Mapping) and proposta else None


# ---------------------------------------------------------------------------
# Consentimento real antes de abrir chamado — checado por CÓDIGO
#
# Lição herdada: uma proposta antiga no estado sem prazo + o cliente citando
# um chamado existente e respondendo "Esse" = chamado novo aberto sem
# consentimento, porque quem decidia o consentimento era o modelo. Agora a
# proposta nasce carimbada (`criada_em`, `turno`) e `abrir` só vale se (1) a
# proposta tem no máximo PROPOSTA_VALIDADE, (2) estamos exatamente no turno
# SEGUINTE ao da proposta e (3) a mensagem do cliente neste turno é uma
# confirmação explícita e SÓ isso.
# ---------------------------------------------------------------------------

#: Validade de uma proposta preparada: vencida, o modelo precisa preparar de
#: novo e pedir a confirmação outra vez.
PROPOSTA_VALIDADE = timedelta(minutes=15)

#: Mensagens (normalizadas: minúsculas, sem acento/pontuação/emoji) que contam
#: como confirmação explícita da proposta. Deliberadamente SEM formas
#: deíticas ("esse", "isso", "aquele") — podem apontar um chamado existente,
#: não a proposta — e sem nada que carregue outro pedido.
CONFIRMACOES_EXPLICITAS: frozenset[str] = frozenset(
    {
        "sim",
        "s",
        "yes",
        "sim sim",
        "ok",
        "okay",
        "okey",
        "oks",
        "okk",
        "ok sim",
        "sim ok",
        "ta ok",
        "pode",
        "pode sim",
        "sim pode",
        "pode ser",
        "sim pode ser",
        "ok pode ser",
        "pode abrir",
        "pode abrir sim",
        "sim pode abrir",
        "ok pode abrir",
        "ok pode",
        "pode abrir o chamado",
        "pode abrir por favor",
        "pode abrir por gentileza",
        "pode abrir sim por favor",
        "abre",
        "abre o chamado",
        "sim abre",
        "pode registrar",
        "sim pode registrar",
        "pode criar",
        "pode fazer",
        "pode prosseguir",
        "prossiga",
        "pode seguir",
        "pode confirmar",
        "pode mandar",
        "manda",
        "confirmo",
        "sim confirmo",
        "confirmado",
        "confirma",
        "confirmar",
        "certo",
        "correto",
        "esta correto",
        "ta certo",
        "esta certo",
        "tudo certo",
        "exato",
        "perfeito",
        "beleza",
        "blz",
        "fechado",
        "fechou",
        "claro",
        "com certeza",
        "positivo",
        "aham",
        "uhum",
        "ta",
        "ta bom",
        "tabom",
        "ta bem",
        "sim por favor",
        "sim por gentileza",
        "por favor",
        "sim sofia",
        "ok sofia",
        "pode abrir sofia",
        "pode sofia",
    }
)

_NAO_ALFANUMERICO = re.compile(r"[^a-z0-9]+")


def _normalizar(texto: Any) -> str:
    """Minúsculas, sem acento, só letras/dígitos separados por um espaço."""
    if not isinstance(texto, str):
        texto = "" if texto is None else str(texto)
    sem_acento = "".join(
        c for c in unicodedata.normalize("NFKD", texto) if not unicodedata.combining(c)
    )
    return _NAO_ALFANUMERICO.sub(" ", sem_acento.lower()).strip()


def eh_confirmacao_explicita(texto: Any) -> bool:
    """True só se a mensagem É uma confirmação ("sim", "ok", "pode abrir"...)
    e nada mais. Um número de chamado, "esse", uma pergunta ou um pedido de
    mudança ("sim, mas muda a prioridade") NÃO confirmam."""
    return _normalizar(texto) in CONFIRMACOES_EXPLICITAS


def _agora() -> datetime:
    """Relógio da política (UTC) — separado para os testes congelarem o tempo."""
    return datetime.now(UTC)


def _turno_atual(state: Mapping[str, Any]) -> int:
    """Número do turno = quantidade de mensagens do cliente no histórico.

    Derivado do histórico persistido (nunca de um contador que o modelo possa
    influenciar): no turno N o `tools_node` vê N `HumanMessage`.
    """
    mensagens: Iterable[BaseMessage] = state.get("messages") or []
    return sum(1 for m in mensagens if isinstance(m, HumanMessage))


def _ultima_mensagem_do_cliente(state: Mapping[str, Any]) -> str:
    for m in reversed(list(state.get("messages") or [])):
        if isinstance(m, HumanMessage):
            conteudo = m.content
            if isinstance(conteudo, list):
                return "".join(b.get("text", "") for b in conteudo if isinstance(b, dict))
            return "" if conteudo is None else str(conteudo)
    return ""


def carimbar_proposta(
    proposta: Mapping[str, Any],
    state: Mapping[str, Any],
    agora: datetime | None = None,
) -> dict[str, Any]:
    """Cópia da proposta aprovada pelo dry-run com `criada_em` (ISO 8601, UTC)
    e `turno` (turno em que foi preparada). É esse carimbo que `abrir` confere.
    """
    quando = agora or _agora()
    return {
        **dict(proposta),
        "criada_em": quando.astimezone(UTC).isoformat(),
        "turno": _turno_atual(state),
    }


def _criada_em(proposta: Mapping[str, Any]) -> datetime | None:
    bruto = proposta.get("criada_em")
    if not isinstance(bruto, str) or not bruto:
        return None
    try:
        quando = datetime.fromisoformat(bruto)
    except ValueError:
        return None
    if quando.tzinfo is None:
        quando = quando.replace(tzinfo=UTC)
    return quando


def _exigir_proposta_confirmada(state: Mapping[str, Any]) -> str | None:
    """Abrir chamado exige proposta preparada, válida e confirmada AGORA.

    Cada recusa diz ao modelo o que fazer — nenhuma delas pede que ele
    "interprete" a vontade do cliente: a confirmação é checada por código.
    """
    proposta = _proposta_da_sessao(state)
    if not proposta:
        return (
            "não há proposta de chamado preparada — chame preparar_abertura_chamado, "
            "apresente o resumo ao cliente e só abra após a confirmação dele"
        )
    criada_em = _criada_em(proposta)
    turno_proposta = proposta.get("turno")
    if criada_em is None or not isinstance(turno_proposta, int):
        return (
            "a proposta guardada não tem validade/turno registrados (foi preparada "
            "por uma versão anterior) — prepare de novo com preparar_abertura_chamado, "
            "apresente o resumo e peça a confirmação"
        )
    if _agora() - criada_em > PROPOSTA_VALIDADE:
        minutos = int(PROPOSTA_VALIDADE.total_seconds() // 60)
        return (
            f"a proposta venceu (vale {minutos} minutos) — prepare de novo com "
            "preparar_abertura_chamado, apresente o resumo e peça a confirmação"
        )
    atual = _turno_atual(state)
    if atual == turno_proposta:
        return (
            "a proposta foi preparada NESTE turno e o cliente ainda não a viu — "
            "apresente o resumo a ele e espere a confirmação na PRÓXIMA mensagem; "
            "não abra agora"
        )
    if atual != turno_proposta + 1:
        return (
            "a confirmação precisa ser a mensagem SEGUINTE à proposta e a conversa "
            "seguiu sem ela — se o cliente ainda quiser o chamado, prepare de novo "
            "com preparar_abertura_chamado e peça a confirmação outra vez"
        )
    if not eh_confirmacao_explicita(_ultima_mensagem_do_cliente(state)):
        return (
            "a última mensagem do cliente NÃO é uma confirmação explícita da "
            "proposta (só valem respostas como 'sim', 'ok', 'pode abrir', sem outro "
            "conteúdo) — um número de chamado, 'esse', uma pergunta ou um pedido de "
            "mudança não confirmam; não abra: responda ao que ele disse e, se for "
            "mudança, prepare de novo e peça a confirmação"
        )
    return None


# ---------------------------------------------------------------------------
# A política em si — uma linha por tool exposta ao agente
# ---------------------------------------------------------------------------

#: Preenchido em E2.3 (tools do Infodesk): `informar_identificacao`,
#: `preparar_abertura_chamado`, `abrir_chamado`, `consultar_status_chamado`,
#: `listar_meus_chamados`, `transferir_para_atendente` — ver a matriz em
#: `docs/plano/01-ferramentas.md` (seção D). Vazio nesta etapa porque
#: `get_default_tools()` também está vazia: nenhuma tool sem política.
POLITICAS: dict[str, PoliticaTool] = {}


def aplicar_politica(
    nome: str, args: Mapping[str, Any], state: Mapping[str, Any]
) -> tuple[dict[str, Any], str | None]:
    """Aplica a política de uma tool call proposta pelo LLM.

    Returns:
        `(args_finais, erro)`. Com `erro != None` a tool NÃO deve ser executada
        — o texto vira o `ToolMessage` devolvido ao agente, que explica a
        recusa ao cliente sem inventar dados.
    """
    politica = POLITICAS.get(nome)
    final = dict(args)
    if politica is None:
        return final, None

    if politica.exigir is not None:
        erro = politica.exigir(state)
        if erro:
            return final, erro

    for arg, validador in politica.validar.items():
        if arg in final:
            erro = validador(final[arg], state)
            if erro:
                return final, erro

    # Injeção por último: sobrescreve QUALQUER valor que o LLM tenha tentado
    # passar num arg de sessão (o schema não os expõe, mas modelos podem
    # alucinar args — a sobrescrita torna a tentativa inócua).
    for arg, resolver in politica.injetar.items():
        final[arg] = resolver(state)
    return final, None
