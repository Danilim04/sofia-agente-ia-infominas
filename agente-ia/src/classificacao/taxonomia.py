"""Taxonomia de atendimento — carrega e valida `config/taxonomia.yaml`.

O YAML é a TABELA que decide mesa, departamento, prioridade e piso de
prioridade por categoria (regra 1 do CLAUDE.md: nunca o LLM). Este módulo:

- valida o formato (`docs/plano/02-classificacao.md`, seção "Taxonomia");
- gera os `criteria` da pergunta `categoria` do JEV;
- avisa no boot enquanto `placeholder: true` (P1).

O cérebro só usa os NOMES das categorias, o piso de prioridade e a descrição;
mesa/departamento/catalogo são resolvidos pelo gateway (E3.1) a partir do
mesmo arquivo — nada disso entra no prompt do LLM.
"""

from __future__ import annotations

import logging
import re
from collections.abc import Mapping, Sequence
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any, Final

import yaml
from yaml.constructor import ConstructorError

from src.classificacao.tipos import CATEGORIA_INDEFINIDA, PRIORIDADES

logger = logging.getLogger(__name__)

_NOME_CATEGORIA: Final = re.compile(r"^[a-z][a-z0-9_]*$")


class ErroTaxonomia(ValueError):
    """Taxonomia inválida (arquivo ausente, formato ou referência quebrada)."""


@dataclass(frozen=True)
class Categoria:
    nome: str
    descricao: str
    mesa: str
    departamento: str
    exemplos: tuple[str, ...] = ()
    nao_e: tuple[str, ...] = ()
    #: Item de catálogo da mesa (opcional; omitir = não enviar — `07` D5).
    catalogo: str | None = None
    #: Prioridade default da categoria (usada quando o JEV não tem confiança).
    prioridade: str | None = None
    #: Piso: vence o Score do JEV quando for mais alto (regra de código).
    prioridade_minima: str | None = None

    def criterio_jev(self) -> dict[str, Any]:
        """Objeto estruturado que a doc do JEV recomenda para cada opção."""
        criterio: dict[str, Any] = {"what": self.descricao}
        if self.nao_e:
            criterio["not_for"] = list(self.nao_e)
        if self.exemplos:
            criterio["examples"] = list(self.exemplos)
        return criterio


@dataclass(frozen=True)
class Taxonomia:
    versao: str
    placeholder: bool
    mesa_triagem: str
    departamento_humano_padrao: str
    categorias: Mapping[str, Categoria]
    #: Nome canônico da prioridade → valor do Infodesk (UUID/nome; `None` = omitir).
    prioridades: Mapping[str, str | None] = field(default_factory=dict)

    @property
    def nomes(self) -> tuple[str, ...]:
        return tuple(self.categorias)

    def criterios_jev(self) -> dict[str, dict[str, Any]]:
        """`criteria` do Choice `categoria`: todas as categorias, `indefinida` inclusive."""
        return {nome: cat.criterio_jev() for nome, cat in self.categorias.items()}

    def existe(self, nome: str) -> bool:
        return nome in self.categorias

    def prioridade_minima(self, categoria: str) -> str | None:
        cat = self.categorias.get(categoria)
        return cat.prioridade_minima if cat else None

    def prioridade_padrao(self, categoria: str) -> str | None:
        cat = self.categorias.get(categoria)
        return cat.prioridade if cat else None

    def descricao(self, categoria: str) -> str:
        cat = self.categorias.get(categoria)
        return cat.descricao if cat else ""


# ---------------------------------------------------------------------------
# Carga e validação
# ---------------------------------------------------------------------------


class _LoaderSemDuplicatas(yaml.SafeLoader):
    """SafeLoader que recusa chave duplicada (o PyYAML padrão sobrescreve em silêncio)."""


def _construir_mapa(
    loader: _LoaderSemDuplicatas, node: yaml.MappingNode, deep: bool = False
) -> Any:
    vistos: set[Any] = set()
    for chave_no, _ in node.value:
        chave = loader.construct_object(chave_no, deep=True)
        if chave in vistos:
            raise ConstructorError(None, None, f"chave duplicada: {chave!r}", chave_no.start_mark)
        vistos.add(chave)
    return yaml.SafeLoader.construct_mapping(loader, node, deep=deep)


_LoaderSemDuplicatas.add_constructor(
    yaml.resolver.BaseResolver.DEFAULT_MAPPING_TAG, _construir_mapa
)


def _texto_obrigatorio(dado: Mapping[str, Any], chave: str, onde: str) -> str:
    valor = dado.get(chave)
    if not isinstance(valor, str) or not valor.strip():
        raise ErroTaxonomia(f"{onde}: '{chave}' é obrigatório (texto não vazio)")
    return valor.strip()


def _lista_de_textos(dado: Mapping[str, Any], chave: str, onde: str) -> tuple[str, ...]:
    valor = dado.get(chave)
    if valor is None:
        return ()
    if not isinstance(valor, Sequence) or isinstance(valor, str):
        raise ErroTaxonomia(f"{onde}: '{chave}' deve ser uma lista de textos")
    itens: list[str] = []
    for item in valor:
        if not isinstance(item, str) or not item.strip():
            raise ErroTaxonomia(f"{onde}: '{chave}' só aceita textos não vazios")
        itens.append(item.strip())
    return tuple(itens)


def _prioridade_opcional(
    dado: Mapping[str, Any], chave: str, onde: str, validas: Sequence[str]
) -> str | None:
    valor = dado.get(chave)
    if valor is None:
        return None
    if valor not in validas:
        raise ErroTaxonomia(
            f"{onde}: '{chave}' = {valor!r} não está em prioridades {list(validas)}"
        )
    return str(valor)


def validar_taxonomia(bruto: Any) -> Taxonomia:
    """Valida o dicionário lido do YAML e devolve a `Taxonomia` imutável."""
    if not isinstance(bruto, Mapping):
        raise ErroTaxonomia("o topo do YAML deve ser um mapa")

    placeholder = bruto.get("placeholder", False)
    if not isinstance(placeholder, bool):
        raise ErroTaxonomia("'placeholder' deve ser booleano")

    prioridades_brutas = bruto.get("prioridades")
    if not isinstance(prioridades_brutas, Mapping):
        raise ErroTaxonomia("'prioridades' é obrigatório (nome canônico → valor do Infodesk)")
    faltam = [p for p in PRIORIDADES if p not in prioridades_brutas]
    sobram = [p for p in prioridades_brutas if p not in PRIORIDADES]
    if faltam or sobram:
        raise ErroTaxonomia(
            f"'prioridades' deve ter exatamente {list(PRIORIDADES)} "
            f"(faltam={faltam} sobram={sobram})"
        )
    prioridades: dict[str, str | None] = {}
    for nome, valor in prioridades_brutas.items():
        if valor is not None and not isinstance(valor, str):
            raise ErroTaxonomia(f"prioridades.{nome}: use texto ou null")
        prioridades[str(nome)] = valor

    categorias_brutas = bruto.get("categorias")
    if not isinstance(categorias_brutas, Mapping) or not categorias_brutas:
        raise ErroTaxonomia("'categorias' é obrigatório e não pode ser vazio")

    categorias: dict[str, Categoria] = {}
    for nome, dado in categorias_brutas.items():
        onde = f"categorias.{nome}"
        if not isinstance(nome, str) or not _NOME_CATEGORIA.match(nome):
            raise ErroTaxonomia(
                f"{onde}: nome deve ser snake_case sem acento ({_NOME_CATEGORIA.pattern})"
            )
        if not isinstance(dado, Mapping):
            raise ErroTaxonomia(f"{onde}: deve ser um mapa")
        catalogo = dado.get("catalogo")
        if catalogo is not None and (not isinstance(catalogo, str) or not catalogo.strip()):
            raise ErroTaxonomia(f"{onde}: 'catalogo' deve ser texto (ou omitido)")
        categorias[nome] = Categoria(
            nome=nome,
            descricao=_texto_obrigatorio(dado, "descricao", onde),
            mesa=_texto_obrigatorio(dado, "mesa", onde),
            departamento=_texto_obrigatorio(dado, "departamento", onde),
            exemplos=_lista_de_textos(dado, "exemplos", onde),
            nao_e=_lista_de_textos(dado, "nao_e", onde),
            catalogo=catalogo.strip() if isinstance(catalogo, str) else None,
            prioridade=_prioridade_opcional(dado, "prioridade", onde, PRIORIDADES),
            prioridade_minima=_prioridade_opcional(dado, "prioridade_minima", onde, PRIORIDADES),
        )
    if CATEGORIA_INDEFINIDA not in categorias:
        raise ErroTaxonomia(
            f"a categoria '{CATEGORIA_INDEFINIDA}' é obrigatória (destino da triagem)"
        )

    return Taxonomia(
        versao=str(bruto.get("versao") or "sem-versao"),
        placeholder=placeholder,
        mesa_triagem=_texto_obrigatorio(bruto, "mesa_triagem", "topo"),
        departamento_humano_padrao=_texto_obrigatorio(bruto, "departamento_humano_padrao", "topo"),
        categorias=categorias,
        prioridades=prioridades,
    )


def carregar_taxonomia(caminho: Path | str | None = None) -> Taxonomia:
    """Lê e valida o YAML (default: `TAXONOMIA_PATH` ou `config/taxonomia.yaml`).

    Avisa em log (WARNING) enquanto `placeholder: true` — P1: as categorias,
    mesas e departamentos ainda são hipóteses até o export do Infodesk.
    """
    if caminho is None:
        from src.classificacao.config import get_config_classificacao

        caminho = get_config_classificacao().taxonomia_path
    arquivo = Path(caminho)
    try:
        texto = arquivo.read_text(encoding="utf-8")
    except OSError as exc:
        raise ErroTaxonomia(f"não foi possível ler {arquivo}: {exc}") from exc
    try:
        bruto = yaml.load(texto, Loader=_LoaderSemDuplicatas)  # noqa: S506  # SafeLoader
    except yaml.YAMLError as exc:
        raise ErroTaxonomia(f"{arquivo}: YAML inválido: {exc}") from exc
    try:
        taxonomia = validar_taxonomia(bruto)
    except ErroTaxonomia as exc:
        raise ErroTaxonomia(f"{arquivo}: {exc}") from exc
    if taxonomia.placeholder:
        # TODO(P1): remover quando vier o export do Infodesk (placeholder: false).
        logger.warning(
            "taxonomia_placeholder versao=%s categorias=%d — mapeamento categoria→mesa "
            "é HIPÓTESE (P1); evals de categoria são informativos",
            taxonomia.versao,
            len(taxonomia.categorias),
        )
    return taxonomia
