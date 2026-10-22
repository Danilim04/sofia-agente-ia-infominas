"""Métricas do cérebro: contadores em memória + log `sofia_metrica` (E4.3).

Mesmo desenho do gateway (`alerta.Vigia.Contar` + log `sofia_metrica`): sem
servidor de métricas — o contador sobe e o log acompanha, o agregador de log
conta/alerta. Zera no restart.

`jev_indisponivel`: o roteador (`classificacao/roteador.py`) já loga o evento
`jev_indisponivel erro=<Tipo>` a cada turno em que o JEV falha e segue em
fail-soft. Este módulo não exige mudar o roteador: um `logging.Handler`
(`ContadorDeEventos`) conta esse evento pelo NOME. Por isso a métrica só conta
com `LOG_LEVEL` ≤ WARNING (o evento é WARNING); com ERROR o contador fica
parado — `operacao.md` avisa.
"""

from __future__ import annotations

import logging
import threading

from src.observability.eventos import JEV_INDISPONIVEL, SOFIA_METRICA

logger = logging.getLogger(__name__)

#: Eventos de log que contam como métrica (nome do evento = nome da métrica).
METRICAS_POR_LOG = frozenset({JEV_INDISPONIVEL})

_contadores: dict[str, int] = {}
_trava = threading.Lock()


def contar(metrica: str) -> int:
    """Sobe o contador, loga `sofia_metrica` e devolve o novo total."""
    with _trava:
        _contadores[metrica] = _contadores.get(metrica, 0) + 1
        total = _contadores[metrica]
    logger.info("%s metrica=%s total=%d", SOFIA_METRICA, metrica, total)
    return total


def contagem(metrica: str) -> int:
    with _trava:
        return _contadores.get(metrica, 0)


def zerar() -> None:
    """Só para testes."""
    with _trava:
        _contadores.clear()


class ContadorDeEventos(logging.Handler):
    """Conta registros cujo primeiro token é um evento de `METRICAS_POR_LOG`."""

    def emit(self, record: logging.LogRecord) -> None:
        try:
            if record.name == logger.name:  # o próprio `sofia_metrica` não se conta
                return
            primeiro = record.getMessage().split(" ", 1)[0]
            if primeiro in METRICAS_POR_LOG:
                contar(primeiro)
        except Exception:  # métrica nunca derruba o atendimento
            self.handleError(record)


def instalar_contador(alvo: logging.Logger | None = None) -> ContadorDeEventos:
    """Liga o contador ao logger `src` (idempotente) e devolve o handler."""
    alvo = alvo or logging.getLogger("src")
    for h in alvo.handlers:
        if isinstance(h, ContadorDeEventos):
            return h
    handler = ContadorDeEventos(level=logging.WARNING)
    alvo.addHandler(handler)
    return handler
