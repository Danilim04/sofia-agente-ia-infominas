"""Política de autorização das tools — "o LLM propõe, o código dispõe".

Princípio (F7/F9 do laudo 2026-08-27): o LLM NUNCA faz parte da base confiável
do sistema. Toda tool call que ele emite é tratada como input não-confiável —
como se tivesse vindo do próprio usuário — e passa por esta política antes de
executar. Identidade e escopo vêm SEMPRE do estado da sessão (resolvido pelo
gate Go via Contrato A), nunca de argumento escolhido pelo modelo.

Este módulo é a fonte única de verdade sobre o que o agente pode fazer:

- ``injetar``: args preenchidos pelo ``tools_node`` a partir do ESTADO da
  sessão. Nas tools eles são ``InjectedToolArg`` — ficam FORA do schema que o
  modelo vê, então não existe sequência de tokens capaz de controlá-los.
  Qualquer valor que o modelo tente passar nesses nomes é sobrescrito.
- ``validar``: args que o LLM fornece legitimamente (ex.: desambiguação de
  empresa) mas que só passam se condizerem com a sessão. Defesa em
  profundidade — o gateway Go revalida server-side.

Uma tool ausente deste mapa não recebe nem valida nada da sessão (tools puras
de conteúdo). Auditoria de segurança das tools começa e termina neste arquivo.
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
    return state.get("telefone") or ""


def _conversation_id_da_sessao(state: Mapping[str, Any]) -> str:
    """Id da conversa (Contrato A) — só correlação de logs no gateway."""
    return str(state.get("conversation_id") or "")


def _proposta_da_sessao(state: Mapping[str, Any]) -> dict | None:
    """Proposta de chamado validada pelo dry-run do gateway (estado do grafo)."""
    return state.get("proposta_chamado") or None


def grupos_da_sessao(state: Mapping[str, Any]) -> list[str]:
    """Grupos de empresa com acesso ativo na sessão (escopo de dados).

    Fonte primária: a identidade resolvida pelo gate (Contrato A). Fallback
    para o harness de dev/testes: o mapeamento mock telefone→grupo. Sessão sem
    escopo devolve lista vazia — as tools escopadas falham FECHADAS.
    """
    identidade = state.get("identidade") or {}
    grupos = [
        emp.get("grupo_empresa")
        for emp in (identidade.get("empresas") or [])
        if emp.get("grupo_empresa")
    ]
    if grupos:
        return grupos
    from src.tools.crm_mocks import grupos_emp_por_telefone

    return grupos_emp_por_telefone(state.get("telefone") or "")


# ---------------------------------------------------------------------------
# Validadores — recusam args do LLM incompatíveis com a sessão
# ---------------------------------------------------------------------------


def _validar_empresa_da_sessao(valor: Any, state: Mapping[str, Any]) -> str | None:
    """`empresa` (desambiguação de chamado) precisa pertencer à sessão."""
    if not valor:
        return None  # vazio = gateway resolve pela identidade; nada a validar
    grupos = {g.strip().upper() for g in grupos_da_sessao(state) if g}
    if str(valor).strip().upper() in grupos:
        return None
    return (
        "empresa fora do escopo desta sessão — o chamado só pode ser aberto "
        "para uma empresa do próprio cliente"
    )


# ---------------------------------------------------------------------------
# Consentimento real antes de abrir chamado (ZB-1) — checado por CÓDIGO
#
# Incidente (conversa #45): uma proposta antiga ficou no estado sem prazo; o
# cliente citou um protocolo existente e respondeu "Esse", e o modelo tratou
# como confirmação — chamado novo aberto sem consentimento. Quem decidia o
# consentimento era o modelo. Agora a proposta nasce carimbada (`criada_em`,
# `turno`) e `abrir` só vale se (1) a proposta tem no máximo PROPOSTA_VALIDADE,
# (2) estamos exatamente no turno SEGUINTE ao da proposta e (3) a mensagem do
# cliente neste turno é uma confirmação explícita e SÓ isso.
# ---------------------------------------------------------------------------

#: Validade de uma proposta preparada: vencida, o modelo precisa preparar de
#: novo e pedir a confirmação outra vez.
PROPOSTA_VALIDADE = timedelta(minutes=15)

#: Mensagens (normalizadas: minúsculas, sem acento/pontuação/emoji) que contam
#: como confirmação explícita da proposta. Deliberadamente SEM formas
#: deíticas ("esse", "isso", "aquele") — no incidente #45 "Esse" apontava um
#: protocolo existente, não a proposta — e sem nada que carregue outro pedido.
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
        "sim zapin",
        "ok zapin",
        "pode abrir zapin",
        "pode zapin",
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
    e nada mais. Um protocolo, "esse", uma pergunta ou um pedido de mudança
    ("sim, mas muda a prioridade") NÃO confirmam."""
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
            "conteúdo) — um protocolo, 'esse', uma pergunta ou um pedido de mudança "
            "não confirmam; não abra: responda ao que ele disse e, se for mudança, "
            "prepare de novo e peça a confirmação"
        )
    return None


# ---------------------------------------------------------------------------
# Chamado novo só com pedido explícito; protocolo citado nunca vira chamado
# novo (ZB-2). O gateway lista os abertos do relator antes de preparar/abrir e
# recusa com `chamado_aberto` a menos que a tool leve `novo_chamado=true`.
# Esse argumento é do modelo — então aqui o código confere se o CLIENTE pediu
# mesmo um chamado novo nesta mensagem (ou confirmou a pergunta do agente
# sobre isso). E uma mensagem que cita um protocolo é sobre um chamado
# EXISTENTE: preparar é recusado e o modelo é mandado listar.
# ---------------------------------------------------------------------------

#: Protocolo do SAC na fala do cliente (ex.: ZPRS25208011): letras + 6..12
#: dígitos colados. Romaneios (4-XXXXXX, RCF-XXXXXX) e NFs (só dígitos) não casam.
PROTOCOLO_RE = re.compile(r"\b[A-Za-z]{2,6}\d{6,12}\b")

_PEDIDO_NOVO_CHAMADO_RE = re.compile(
    r"\b(novo|outro|mais um|segundo|um novo|um outro) (chamado|ticket)\b"
    r"|\b(chamado|ticket) (novo|separado|a parte)\b"
    r"|\babr(e|a|ir|indo) (um |mais um |outro |um outro |um novo )?(novo|outro)\b"
    r"|\b(criar?|registrar?|cria|registra) (um |mais um |um outro |um novo )?(novo|outro)\b"
)

_PERGUNTA_NOVO_CHAMADO_RE = re.compile(
    r"\b(novo|outro|mais um) chamado\b|\bchamado novo\b|\bmesmo assim\b"
)


def protocolo_citado(texto: Any) -> str | None:
    """Protocolo de chamado presente na mensagem, se houver (maiúsculo)."""
    if not isinstance(texto, str):
        return None
    m = PROTOCOLO_RE.search(texto)
    return m.group(0).upper() if m else None


def _ultima_resposta_do_agente_antes_da_mensagem_atual(
    state: Mapping[str, Any],
) -> str:
    """Texto da última resposta do agente ANTES da mensagem atual do cliente."""
    msgs = list(state.get("messages") or [])
    idx = None
    for i in range(len(msgs) - 1, -1, -1):
        if isinstance(msgs[i], HumanMessage):
            idx = i
            break
    if not idx:
        return ""
    for m in reversed(msgs[:idx]):
        if isinstance(m, HumanMessage):
            return ""
        conteudo = getattr(m, "content", None)
        if m.type == "ai" and isinstance(conteudo, str) and conteudo.strip():
            return conteudo
        if m.type == "ai" and isinstance(conteudo, list):
            texto = "".join(b.get("text", "") for b in conteudo if isinstance(b, dict))
            if texto.strip():
                return texto
    return ""


def cliente_pediu_novo_chamado(state: Mapping[str, Any]) -> bool:
    """True se o cliente pediu EXPLICITAMENTE um chamado novo: na própria
    mensagem ("quero abrir outro chamado") ou confirmando ("sim") a pergunta
    do agente que mencionou um chamado novo / "mesmo assim"."""
    atual = _normalizar(_ultima_mensagem_do_cliente(state))
    if _PEDIDO_NOVO_CHAMADO_RE.search(atual):
        return True
    if eh_confirmacao_explicita(atual):
        pergunta = _normalizar(_ultima_resposta_do_agente_antes_da_mensagem_atual(state))
        return bool(_PERGUNTA_NOVO_CHAMADO_RE.search(pergunta))
    return False


def _validar_pedido_de_novo_chamado(valor: Any, state: Mapping[str, Any]) -> str | None:
    """`novo_chamado=true` só com pedido explícito do cliente, checado por código."""
    if not valor:
        return None
    if cliente_pediu_novo_chamado(state):
        return None
    return (
        "novo_chamado=true só vale quando o CLIENTE pediu explicitamente um chamado "
        "novo nesta mensagem (ex.: 'quero abrir outro chamado') ou confirmou sua "
        "pergunta sobre abrir um novo — ele não fez isso; mostre os chamados abertos "
        "(protocolo, resumo e link), pergunte se o problema é de um deles e só "
        "repita com novo_chamado=true se ele pedir um novo"
    )


def _exigir_sem_protocolo_citado(state: Mapping[str, Any]) -> str | None:
    """Mensagem do cliente com protocolo é sobre um chamado EXISTENTE."""
    protocolo = protocolo_citado(_ultima_mensagem_do_cliente(state))
    if not protocolo:
        return None
    return (
        f"o cliente citou o protocolo {protocolo} — isso é um chamado EXISTENTE, "
        "não um pedido de chamado novo: chame listar_chamados_abertos, mostre o "
        "link desse chamado e oriente a continuar pelo chat dele; não prepare um "
        "chamado novo a partir desta mensagem"
    )


# ---------------------------------------------------------------------------
# A política em si — uma linha por tool exposta ao agente
# ---------------------------------------------------------------------------

POLITICAS: dict[str, PoliticaTool] = {
    # SAC (gateway Go): o relator vem da identidade do gate via `telefone`;
    # `conversation_id` só liga o log do gateway à conversa (ZA-3).
    "consultar_tipos_de_chamado": PoliticaTool(
        injetar={
            "telefone": _telefone_da_sessao,
            "conversation_id": _conversation_id_da_sessao,
        },
    ),
    "listar_chamados_abertos": PoliticaTool(
        injetar={
            "telefone": _telefone_da_sessao,
            "conversation_id": _conversation_id_da_sessao,
        },
    ),
    # Duas fases (dry-run → confirmação → execução): `preparar` valida os
    # campos no gateway e a proposta aprovada fica no estado (carimbada com
    # `criada_em`/`turno`); `abrir` não recebe NADA do modelo — executa
    # exatamente a proposta preparada, e só com a confirmação explícita do
    # cliente na mensagem seguinte, dentro da validade (ZB-1).
    "preparar_abertura_chamado": PoliticaTool(
        injetar={
            "telefone": _telefone_da_sessao,
            "conversation_id": _conversation_id_da_sessao,
        },
        validar={
            "empresa": _validar_empresa_da_sessao,
            "novo_chamado": _validar_pedido_de_novo_chamado,
        },
        exigir=_exigir_sem_protocolo_citado,
    ),
    "abrir_chamado_suporte": PoliticaTool(
        injetar={
            "telefone": _telefone_da_sessao,
            "conversation_id": _conversation_id_da_sessao,
            "proposta": _proposta_da_sessao,
        },
        exigir=_exigir_proposta_confirmada,
    ),
    # Rastreio de NF: a tool mock SAIU da lista canônica (ZE-1, rodada 1 — não
    # é exposta ao modelo). A política fica como rede de segurança F7: se uma
    # integração real entrar com o mesmo nome, o escopo continua injetado da
    # sessão, nunca argumento do LLM.
    "rastrear_nota_fiscal": PoliticaTool(
        injetar={"grupos_emp_sessao": grupos_da_sessao},
    ),
}


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
