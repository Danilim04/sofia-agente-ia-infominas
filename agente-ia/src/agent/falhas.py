"""Classificação de falhas do cérebro e ID de ocorrência.

Quando o grafo explode (LLM fora, saldo do OpenRouter esgotado, bug), o
`server.py` NÃO deixa a exceção virar um 500 mudo: classifica a falha e devolve
ao gateway Go a classe + um ID de ocorrência, para o gateway decidir entre
tentar de novo (transitória) e transferir ao atendente na hora (definitiva) —
e para o mesmo ID aparecer no log do cérebro, no log do gateway, na nota
privada da conversa e na tabela `gateway.falhas`.

Classes:

- ``transitoria``: timeout, 429 (rate limit), 5xx do provedor, falha de
  conexão — vale a pena repetir em instantes.
- ``definitiva``: 402 (crédito), 401/403 (chave), 400 (requisição inválida) e
  erro de código (qualquer exceção que não seja de transporte) — repetir não
  resolve; só o atendente humano e um alerta ao responsável.

A classificação é por *duck typing* (``status_code`` no erro ou na cadeia de
``__cause__``) para não depender da hierarquia de exceções do SDK do OpenAI —
e para ser testável com exceções sintéticas.
"""

from __future__ import annotations

import random
import socket
from collections.abc import Iterable
from datetime import UTC, datetime
from zoneinfo import ZoneInfo

#: Prefixo dos IDs de ocorrência — o mesmo nos dois processos (cérebro e
#: gateway Go); mudar aqui exige mudar lá.
PREFIXO_OCORRENCIA = "SOF"

CLASSE_TRANSITORIA = "transitoria"
CLASSE_DEFINITIVA = "definitiva"

# Status HTTP do provedor que valem nova tentativa.
_STATUS_TRANSITORIOS = frozenset({408, 409, 425, 429, 500, 502, 503, 504})
# Nomes de exceção de transporte (sem importar o SDK): casam por sufixo do
# nome da classe, em qualquer ponto da cadeia de causas.
_NOMES_TRANSITORIOS = (
    "Timeout",
    "TimeoutError",
    "TimeoutException",
    "ConnectError",
    "ConnectionError",
    "APIConnectionError",
    "RemoteProtocolError",
    "ReadError",
    "WriteError",
    "PoolTimeout",
    "RateLimitError",
    "InternalServerError",
)

# Causa do estouro do prazo do turno (asyncio.wait_for em processar_chat).
CAUSA_PRAZO_TURNO = "cérebro não concluiu o turno dentro do prazo (BRAIN_TURNO_PRAZO)"

_DESCRICAO_STATUS = {
    400: "requisição inválida ao provedor (400)",
    401: "chave do OpenRouter inválida (401)",
    402: "saldo do OpenRouter insuficiente (402)",
    403: "chave do OpenRouter sem permissão (403)",
    404: "modelo não encontrado no provedor (404)",
    408: "timeout no provedor (408)",
    429: "limite de requisições do provedor (429)",
}


def gerar_id_ocorrencia(agora: datetime | None = None) -> str:
    """ID curto e legível de ocorrência: ``SOF-AAAAMMDD-HHMM-NN`` (Brasília).

    Legível por humanos (o atendente lê na nota privada e o responsável acha
    no log). O sufixo NN é aleatório — a data/hora já isola a ocorrência; o
    sufixo só separa falhas do mesmo minuto. O gateway Go gera no MESMO
    formato quando a falha é dele (Infodesk, disjuntor).
    """
    if agora is None:
        try:
            agora = datetime.now(ZoneInfo("America/Sao_Paulo"))
        except Exception:
            agora = datetime.now(UTC).astimezone()
    return f"{PREFIXO_OCORRENCIA}-{agora:%Y%m%d}-{agora:%H%M}-{random.randint(0, 99):02d}"


def _cadeia(exc: BaseException) -> Iterable[BaseException]:
    """A exceção e suas causas (``__cause__``/``__context__``), sem ciclos."""
    vistos: set[int] = set()
    atual: BaseException | None = exc
    while atual is not None and id(atual) not in vistos:
        vistos.add(id(atual))
        yield atual
        atual = atual.__cause__ or atual.__context__


def _status_de(exc: BaseException) -> int | None:
    for e in _cadeia(exc):
        status = getattr(e, "status_code", None)
        if isinstance(status, int):
            return status
        resposta = getattr(e, "response", None)
        status = getattr(resposta, "status_code", None)
        if isinstance(status, int):
            return status
    return None


def _eh_transporte(exc: BaseException) -> bool:
    for e in _cadeia(exc):
        if isinstance(e, (TimeoutError, ConnectionError, socket.timeout)):
            return True
        nome = type(e).__name__
        if any(nome.endswith(sufixo) for sufixo in _NOMES_TRANSITORIOS):
            return True
    return False


def classificar_falha(exc: BaseException) -> tuple[str, str]:
    """Devolve ``(classe, causa)`` para uma exceção do turno.

    ``causa`` é curta e em linguagem simples (vai para a nota privada do
    atendente e para o alerta) — sem traceback, sem dado do cliente.
    """
    status = _status_de(exc)
    if status is not None:
        descricao = _DESCRICAO_STATUS.get(status)
        if status in _STATUS_TRANSITORIOS:
            return CLASSE_TRANSITORIA, descricao or f"erro temporário do provedor ({status})"
        return CLASSE_DEFINITIVA, descricao or f"erro do provedor ({status})"
    if _eh_transporte(exc):
        return CLASSE_TRANSITORIA, f"falha de conexão/timeout com o provedor ({type(exc).__name__})"
    return CLASSE_DEFINITIVA, f"erro interno do cérebro ({type(exc).__name__})"
