# mypy: disallow-untyped-defs, disallow-incomplete-defs, check-untyped-defs, disallow-any-generics, warn-return-any, strict-equality
"""Cenários do juiz (`evals/conversa/juiz/*.yaml`): conversa SEM script de LLM + rubrica.

O cenário reaproveita o formato da conversa determinística: `sessao`, `jev` (uma
resposta do `FakeJEV` por turno), `relogio` e, por turno, `cliente` + as extensões
`rag`, `gateway` e `avanca_minutos`. Não tem `llm:` (quem responde é o LLM real) nem
`espera:` (quem avalia é o juiz). No lugar, tem:

```yaml
origem: conversa/abre-ticket-apos-confirmacao.yaml   # cenário determinístico de onde veio
rubrica:
  criterios: [persona, exatidao, nao_inventa, pede_confirmacao]   # os que se aplicam
  esperado: "o que uma resposta excelente faz"
  nao_deve: ["o que derruba a nota"]
```

Para validar o resto sem reescrever validador, o YAML ganha um script de LLM de
mentira (`texto` + `espera`) e passa por `evals.conversa.extensoes`, o mesmo caminho
dos cenários determinísticos; esse script não é usado na execução.
"""

from __future__ import annotations

import copy
from collections.abc import Sequence
from dataclasses import dataclass, fields
from pathlib import Path
from typing import Any, Final

import yaml

from evals.conversa.extensoes import CenarioEval, _extrair
from evals.formatos import ErroFormato, carregar_taxonomia, validar_cenario

DIRETORIO_PADRAO: Final[Path] = Path(__file__).resolve().parent.parent / "conversa" / "juiz"

#: Critérios da rubrica (docs/plano/02 e 04: persona, exatidão, não inventa, pede confirmação).
CRITERIOS: Final[tuple[str, ...]] = ("persona", "exatidao", "nao_inventa", "pede_confirmacao")
MINIMO_CENARIOS: Final[int] = 20

_CHAVES_CENARIO: Final[frozenset[str]] = frozenset({"origem", "rubrica"})
_CHAVES_RUBRICA: Final[frozenset[str]] = frozenset({"criterios", "esperado", "nao_deve"})


@dataclass(frozen=True)
class Rubrica:
    criterios: tuple[str, ...]
    esperado: str
    nao_deve: tuple[str, ...]


@dataclass(frozen=True)
class CenarioJuiz:
    """Um `CenarioEval` (sem script de LLM) mais a rubrica do juiz."""

    base: CenarioEval
    rubrica: Rubrica
    origem: str | None

    @property
    def id(self) -> str:
        return self.base.id

    @property
    def descricao(self) -> str:
        return self.base.descricao

    @property
    def arquivo(self) -> Path | None:
        return self.base.arquivo


def _rubrica(bruto: object, campo: str) -> Rubrica:
    if not isinstance(bruto, dict):
        raise ErroFormato(campo, "deve ser um mapeamento")
    extras = sorted(set(bruto) - _CHAVES_RUBRICA)
    if extras:
        raise ErroFormato(f"{campo}.{extras[0]}", f"chave desconhecida ({sorted(_CHAVES_RUBRICA)})")
    criterios = bruto.get("criterios")
    if not isinstance(criterios, list) or not criterios:
        raise ErroFormato(f"{campo}.criterios", "lista não vazia obrigatória")
    for i, c in enumerate(criterios):
        if c not in CRITERIOS:
            raise ErroFormato(f"{campo}.criterios[{i}]", f"'{c}' não está em {list(CRITERIOS)}")
    if len(set(criterios)) != len(criterios):
        raise ErroFormato(f"{campo}.criterios", "critério repetido")
    esperado = bruto.get("esperado")
    if not isinstance(esperado, str) or not esperado.strip():
        raise ErroFormato(f"{campo}.esperado", "texto obrigatório")
    nao_deve = bruto.get("nao_deve")
    if not isinstance(nao_deve, list) or not nao_deve:
        raise ErroFormato(
            f"{campo}.nao_deve", "lista não vazia obrigatória (todo cenário tem negativo)"
        )
    for i, item in enumerate(nao_deve):
        if not isinstance(item, str) or not item.strip():
            raise ErroFormato(f"{campo}.nao_deve[{i}]", "texto obrigatório")
    return Rubrica(tuple(criterios), esperado.strip(), tuple(nao_deve))


def validar_cenario_juiz(
    lido: object, nome: str, categorias: frozenset[str] | None = None, arquivo: Path | None = None
) -> CenarioJuiz:
    if not isinstance(lido, dict):
        raise ErroFormato(nome, "o cenário deve ser um mapeamento YAML")
    bruto: dict[str, Any] = copy.deepcopy(lido)
    for chave in ("llm", "espera"):
        for i, turno in enumerate(bruto.get("turnos") or []):
            if isinstance(turno, dict) and chave in turno:
                raise ErroFormato(
                    f"{nome}.turnos[{i}].{chave}",
                    "cenário do juiz não tem script de LLM nem asserts: o LLM é real e o juiz avalia",
                )
    rubrica = _rubrica(bruto.pop("rubrica", None), f"{nome}.rubrica")
    origem = bruto.pop("origem", None)
    if origem is not None and not isinstance(origem, str):
        raise ErroFormato(f"{nome}.origem", "deve ser texto")
    # Script de mentira só para reaproveitar a validação base (nunca é executado).
    for turno in bruto.get("turnos") or []:
        if isinstance(turno, dict):
            turno["llm"] = [{"texto": "-"}]
            turno["espera"] = {"contem": ["-"]}
    extras = _extrair(bruto, nome)
    base = validar_cenario(bruto, campo=nome, categorias=categorias, arquivo=arquivo)
    cenario = CenarioEval(**{f.name: getattr(base, f.name) for f in fields(base)}, extras=extras)
    return CenarioJuiz(base=cenario, rubrica=rubrica, origem=origem)


def carregar_cenario_juiz(caminho: Path, categorias: frozenset[str] | None = None) -> CenarioJuiz:
    with caminho.open(encoding="utf-8") as f:
        try:
            lido = yaml.safe_load(f)
        except yaml.YAMLError as e:
            raise ErroFormato(caminho.name, f"YAML inválido: {e}") from e
    return validar_cenario_juiz(lido, caminho.name, categorias, caminho)


def carregar_cenarios_juiz(
    diretorio: Path = DIRETORIO_PADRAO, categorias: frozenset[str] | None = None
) -> list[CenarioJuiz]:
    """Carrega e valida todos os `*.yaml` do diretório; ids únicos."""
    if categorias is None:
        categorias = carregar_taxonomia().categorias
    cenarios = [carregar_cenario_juiz(p, categorias) for p in sorted(diretorio.glob("*.yaml"))]
    vistos: dict[str, Path | None] = {}
    for c in cenarios:
        if c.id in vistos:
            raise ErroFormato(c.arquivo.name if c.arquivo else c.id, f"id '{c.id}' repetido")
        vistos[c.id] = c.arquivo
    return cenarios


def selecionar(cenarios: Sequence[CenarioJuiz], ids: Sequence[str]) -> list[CenarioJuiz]:
    """Filtra por id (ou nome do arquivo sem extensão); id desconhecido é erro."""
    if not ids:
        return list(cenarios)
    escolhidos: list[CenarioJuiz] = []
    for pedido in ids:
        achou = [
            c
            for c in cenarios
            if c.id == pedido or (c.arquivo is not None and c.arquivo.stem == pedido)
        ]
        if not achou:
            raise ErroFormato("--ids", f"cenário '{pedido}' não existe")
        escolhidos.extend(achou)
    return escolhidos
