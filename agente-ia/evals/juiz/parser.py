# mypy: disallow-untyped-defs, disallow-incomplete-defs, check-untyped-defs, disallow-any-generics, warn-return-any, strict-equality
"""Parser da resposta do juiz: JSON com nota (0–5) e justificativa curta por critério.

Tolerante ao que modelos fazem (bloco ```json, texto antes/depois do objeto, nota
como "4" ou 4.0), rígido no que importa: todo critério pedido tem de vir, com nota
inteira entre 0 e 5. Qualquer outra coisa levanta `ErroJuiz` — o runner conta o
cenário como "juiz inválido" em vez de inventar nota.
"""

from __future__ import annotations

import json
import re
from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from typing import Any, Final

NOTA_MIN: Final[int] = 0
NOTA_MAX: Final[int] = 5
#: Nota a partir da qual o critério conta como "aprovado" (taxa de aprovação do relatório).
NOTA_APROVACAO: Final[int] = 4
MAX_JUSTIFICATIVA: Final[int] = 240

_CERCA = re.compile(r"```(?:json)?\s*(.*?)```", re.DOTALL | re.IGNORECASE)


class ErroJuiz(ValueError):
    """A resposta do juiz não segue o formato (o runner não adivinha nota)."""


@dataclass(frozen=True)
class NotaCriterio:
    nota: int
    justificativa: str

    @property
    def pontos(self) -> float:
        """Nota na escala 0–100 (a das métricas do relatório)."""
        return 100.0 * self.nota / NOTA_MAX


@dataclass(frozen=True)
class Julgamento:
    notas: Mapping[str, NotaCriterio]


def _objeto_json(texto: str) -> dict[str, Any]:
    candidatos = [m.group(1) for m in _CERCA.finditer(texto)] + [texto]
    decodificador = json.JSONDecoder()
    for candidato in candidatos:
        inicio = candidato.find("{")
        while inicio != -1:
            try:
                objeto, _ = decodificador.raw_decode(candidato[inicio:])
            except ValueError:
                inicio = candidato.find("{", inicio + 1)
                continue
            if isinstance(objeto, dict):
                return objeto
            inicio = candidato.find("{", inicio + 1)
    raise ErroJuiz("a resposta do juiz não contém um objeto JSON")


def _nota(valor: object, criterio: str) -> int:
    if isinstance(valor, bool):
        raise ErroJuiz(f"{criterio}: nota booleana")
    if isinstance(valor, str):
        texto = valor.strip().split("/")[0].strip()
        try:
            valor = float(texto.replace(",", "."))
        except ValueError as e:
            raise ErroJuiz(f"{criterio}: nota '{valor}' não é número") from e
    if not isinstance(valor, int | float):
        raise ErroJuiz(f"{criterio}: nota ausente ou de tipo inválido")
    if float(valor) != int(valor):
        raise ErroJuiz(f"{criterio}: nota {valor} não é inteira")
    nota = int(valor)
    if not NOTA_MIN <= nota <= NOTA_MAX:
        raise ErroJuiz(f"{criterio}: nota {nota} fora de {NOTA_MIN}–{NOTA_MAX}")
    return nota


def parse_resposta_juiz(texto: str, criterios: Sequence[str]) -> Julgamento:
    """Lê a resposta do juiz e devolve a nota de cada critério pedido.

    Critério extra na resposta é ignorado; critério pedido e ausente é erro. A
    justificativa é cortada em `MAX_JUSTIFICATIVA` caracteres.
    """
    objeto = _objeto_json(texto)
    bloco = objeto.get("criterios", objeto)
    if not isinstance(bloco, dict):
        raise ErroJuiz("'criterios' deve ser um objeto")
    notas: dict[str, NotaCriterio] = {}
    for criterio in criterios:
        item = bloco.get(criterio)
        if item is None:
            raise ErroJuiz(f"critério '{criterio}' ausente na resposta do juiz")
        if isinstance(item, dict):
            valor, justificativa = item.get("nota"), item.get("justificativa", "")
        else:  # forma curta {"persona": 4}
            valor, justificativa = item, ""
        if not isinstance(justificativa, str):
            justificativa = str(justificativa)
        justificativa = " ".join(justificativa.split())[:MAX_JUSTIFICATIVA]
        notas[criterio] = NotaCriterio(_nota(valor, criterio), justificativa)
    return Julgamento(notas)
