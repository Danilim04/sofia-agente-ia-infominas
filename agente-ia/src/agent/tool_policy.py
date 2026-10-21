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

Matriz de políticas das 6 tools do Infodesk (E2.3,
`docs/plano/01-ferramentas.md`, seção D) no fim do módulo, em ``POLITICAS``.
O carimbo da proposta mora em ``src/tools/proposta.py`` (a camada ``tools`` não
pode importar a política) e é re-exportado aqui.
"""

from __future__ import annotations

import re
import unicodedata
from collections.abc import Callable, Mapping
from dataclasses import dataclass, field
from datetime import UTC, datetime, timedelta
from typing import Any

from langchain_core.messages import HumanMessage

from src.classificacao.config import ConfigClassificacao
from src.classificacao.tipos import CATEGORIA_INDEFINIDA
from src.identity.config import ident_exigir_cpf
from src.identity.contrato import contrato_valido
from src.identity.cpf import cpf_valido
from src.tools import proposta as _proposta_mod
from src.tools.proposta import carimbar_proposta, turno_atual
from src.tools.validacao import (
    DESCRICAO_MAX,
    MOTIVOS_TRANSFERENCIA,
    TITULO_MAX,
    normalizar_numero_chamado,
)

__all__ = [
    "CONFIRMACOES_EXPLICITAS",
    "POLITICAS",
    "PROPOSTA_VALIDADE",
    "PoliticaTool",
    "aplicar_politica",
    "carimbar_proposta",
    "eh_confirmacao_explicita",
]

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


def classificacao_da_sessao(state: Mapping[str, Any]) -> dict[str, Any] | None:
    """Classificação fixada pelo nó `classificar` (JEV/fallback) — as 5 chaves do
    Contrato A. Mesa, departamento e prioridade de destino saem dela NO GATEWAY
    (tabela da taxonomia); o modelo não escolhe nenhuma."""
    c = state.get("classificacao")
    if not isinstance(c, Mapping) or not c:
        return None
    return {k: c.get(k) for k in ("intencao", "categoria", "prioridade", "confianca", "origem")}


def identificacao_da_sessao(state: Mapping[str, Any]) -> dict[str, str] | None:
    """Visão MASCARADA da identificação do cliente: `{forma, valor_mascarado}`.

    Prefere o `contato` que o gateway mandou (lookup por telefone, CPF ou
    contrato em cache) e cai no registro feito NESTA conversa por
    `informar_identificacao`. Nunca o dado em claro (regra 5 do CLAUDE.md); o
    gateway resolve a identidade real pela tabela `contatos` via telefone.
    """
    for dado in (state.get("contato"), state.get("identificacao")):
        if isinstance(dado, Mapping) and dado.get("forma"):
            return {
                "forma": str(dado["forma"]),
                "valor_mascarado": str(dado.get("valor_mascarado") or ""),
            }
    return None


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
    return _proposta_mod.relogio()


def _turno_atual(state: Mapping[str, Any]) -> int:
    """Número do turno (ver `src.tools.proposta.turno_atual`)."""
    return turno_atual(state)


def _ultima_mensagem_do_cliente(state: Mapping[str, Any]) -> str:
    for m in reversed(list(state.get("messages") or [])):
        if isinstance(m, HumanMessage):
            conteudo = m.content
            if isinstance(conteudo, list):
                return "".join(b.get("text", "") for b in conteudo if isinstance(b, dict))
            return "" if conteudo is None else str(conteudo)
    return ""


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


def exigir_proposta_confirmada(state: Mapping[str, Any]) -> str | None:
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


_exigir_proposta_confirmada = exigir_proposta_confirmada  # nome usado pelos fakes de teste


# ---------------------------------------------------------------------------
# Precondições de sessão do `preparar_abertura_chamado`
# ---------------------------------------------------------------------------

#: "chamado 4821", "protocolo nº 123456", "o protocolo é 20240001234", "chamado de
#: número 4.821", "ordem de serviço 9981": o cliente está falando de um chamado
#: EXISTENTE (regra ZB herdada) — não vira chamado novo. Aplicada sobre o texto
#: normalizado (minúsculas, sem acento, pontuação vira espaço: "4.821" → "4 821").
#: Palavra-chave + até alguns conectivos + número de 3+ dígitos (ou agrupado em
#: milhares). "OS" solta NÃO entra: "tenho os 200 mega" não é protocolo.
#: TODO(P9): o histórico real mostra como o cliente chama o número ("protocolo"?).
_PROTOCOLO_CITADO = re.compile(
    r"\b(?:chamado|protocolo|ticket|ordem de servico)"
    r"(?:\s+(?:n|no|numero|de|e|eh|que))*"
    r"\s+(?:\d{3,}|\d{1,3}(?:\s\d{3})+)\b"
)


def exigir_identificado(state: Mapping[str, Any]) -> str | None:
    """Cliente identificado: por lookup do telefone OU por CPF/contrato validado.

    ``contato.forma == "telefone"`` (lookup do Infodesk, ADR 0004 revisado) basta
    quando ``IDENT_EXIGIR_CPF=false`` (default; TODO(N3): decisão da Infominas);
    com ``true`` só CPF/contrato contam. A forma vem do gateway/da tool de
    identificação — nunca de texto do modelo.
    """
    for dado in (state.get("contato"), state.get("identificacao")):
        forma = dado.get("forma") if isinstance(dado, Mapping) else None
        if forma in ("cpf", "contrato"):
            return None
        if forma == "telefone" and not ident_exigir_cpf():
            return None
    return (
        "o cliente ainda não se identificou — peça o CPF ou o número do contrato e "
        "registre com informar_identificacao antes de preparar o chamado"
    )


def _acao_indefinida() -> str:
    try:
        return ConfigClassificacao().acao_indefinida  # type: ignore[call-arg]
    except Exception:
        return "transferir"


def exigir_categoria(state: Mapping[str, Any]) -> str | None:
    """Categoria definida pelo JEV/fallback ou por esclarecimento — nunca pelo modelo.

    ``indefinida`` só passa se a Infominas escolher a mesa de triagem
    (``JEV_ACAO_INDEFINIDA=triagem``); no default o sistema transfere.
    TODO(P11): decisão "transferir × triagem" para categoria indefinida.
    """
    classificacao = state.get("classificacao")
    categoria = classificacao.get("categoria") if isinstance(classificacao, Mapping) else None
    if categoria and categoria != CATEGORIA_INDEFINIDA:
        return None
    if categoria == CATEGORIA_INDEFINIDA and _acao_indefinida() == "triagem":
        return None
    return (
        "a categoria do problema ainda não está definida — faça UMA pergunta de "
        "esclarecimento ao cliente (o que está acontecendo, desde quando) antes de preparar"
    )


def exigir_sem_protocolo_citado(state: Mapping[str, Any]) -> str | None:
    """Mensagem que cita um número de chamado é sobre chamado existente."""
    if _PROTOCOLO_CITADO.search(_normalizar(_ultima_mensagem_do_cliente(state))):
        return (
            "o cliente citou o número de um chamado existente — não prepare um chamado "
            "novo; use consultar_status_chamado e oriente a acompanhar por ele"
        )
    return None


def _todas(*precondicoes: Precondicao) -> Precondicao:
    """Primeira recusa vence; a ordem das precondições é a ordem do fluxo."""

    def _exigir(state: Mapping[str, Any]) -> str | None:
        for precondicao in precondicoes:
            erro = precondicao(state)
            if erro:
                return erro
        return None

    return _exigir


# ---------------------------------------------------------------------------
# Validadores — args do LLM (defesa em profundidade; o gateway revalida)
# ---------------------------------------------------------------------------


def _vazio(valor: Any) -> bool:
    return valor is None or str(valor).strip() == ""


def validar_cpf(valor: Any, state: Mapping[str, Any]) -> str | None:
    """CPF com 11 dígitos e dígitos verificadores corretos (vazio = não informado)."""
    if _vazio(valor) or cpf_valido(valor):
        return None
    return "CPF inválido; peça para o cliente conferir os 11 dígitos e mandar de novo"


def validar_contrato(valor: Any, state: Mapping[str, Any]) -> str | None:
    """Contrato casa com `IDENT_CONTRATO_RE` (vazio = não informado)."""
    if _vazio(valor) or contrato_valido(valor):
        return None
    return "número de contrato inválido; peça para o cliente conferir e mandar de novo"


def validar_numero(valor: Any, state: Mapping[str, Any]) -> str | None:
    """Número do chamado: só dígitos, 1 a 10."""
    if normalizar_numero_chamado(valor) is not None:
        return None
    return "número de chamado inválido (só dígitos, até 10); peça para o cliente conferir"


def validar_motivo(valor: Any, state: Mapping[str, Any]) -> str | None:
    """Motivo da transferência: só os do enum do plano (A6)."""
    if valor in MOTIVOS_TRANSFERENCIA:
        return None
    return f"motivo inválido; use um de: {', '.join(MOTIVOS_TRANSFERENCIA)}"


def validar_titulo(valor: Any, state: Mapping[str, Any]) -> str | None:
    texto = "" if valor is None else str(valor).strip()
    if not texto or len(texto) > TITULO_MAX or "\n" in texto:
        return f"título obrigatório, em uma linha e com até {TITULO_MAX} caracteres"
    return None


def validar_descricao(valor: Any, state: Mapping[str, Any]) -> str | None:
    if _vazio(valor) or len(str(valor).strip()) > DESCRICAO_MAX:
        return f"descrição obrigatória e com até {DESCRICAO_MAX} caracteres"
    return None


# ---------------------------------------------------------------------------
# A política em si — uma linha por tool exposta ao agente
# ---------------------------------------------------------------------------

_SESSAO = {"telefone": _telefone_da_sessao, "conversation_id": _conversation_id_da_sessao}

#: Matriz tool × política (`docs/plano/01-ferramentas.md`, seção D). Uma linha por
#: tool exposta ao agente; `get_default_tools()` e este mapa têm as mesmas 6 chaves
#: (`tests/test_tool_policy.py`).
POLITICAS: dict[str, PoliticaTool] = {
    "informar_identificacao": PoliticaTool(
        injetar=dict(_SESSAO),
        validar={"cpf": validar_cpf, "contrato": validar_contrato},
    ),
    "preparar_abertura_chamado": PoliticaTool(
        injetar={
            **_SESSAO,
            "classificacao": classificacao_da_sessao,
            "identificacao": identificacao_da_sessao,
        },
        validar={"titulo": validar_titulo, "descricao": validar_descricao},
        exigir=_todas(exigir_identificado, exigir_categoria, exigir_sem_protocolo_citado),
    ),
    "abrir_chamado": PoliticaTool(
        injetar={**_SESSAO, "proposta": _proposta_da_sessao},
        exigir=exigir_proposta_confirmada,
    ),
    "consultar_status_chamado": PoliticaTool(
        injetar={**_SESSAO, "identificacao": identificacao_da_sessao},
        validar={"numero": validar_numero},
    ),
    "listar_meus_chamados": PoliticaTool(injetar=dict(_SESSAO)),
    "transferir_para_atendente": PoliticaTool(
        injetar={**_SESSAO, "classificacao": classificacao_da_sessao},
        validar={"motivo": validar_motivo},
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
