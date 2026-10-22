"""Taxonomia PROPOSTA (`config/taxonomia.proposta.yaml`) — E5.1, preparação.

A proposta ainda não é a taxonomia em uso (`config/taxonomia.yaml` segue placeholder):
estes testes provam que ela é carregável, que cada mesa, prioridade, item de catálogo e
departamento existe de verdade no Infodesk (fixture `tests/fixtures/infodesk_referencia.json`,
só nomes e ids), que as decisões pendentes estão marcadas no arquivo e que os cenários de
conversa de origem real rodam contra ela. Sem rede.
"""

from __future__ import annotations

import json
import re
from collections.abc import Iterator
from pathlib import Path
from typing import Any

import pytest

from evals.conversa import runner
from evals.conversa.extensoes import carregar_cenario_eval
from evals.formatos import carregar_taxonomia as carregar_taxonomia_evals
from src.classificacao.config import get_config_classificacao
from src.classificacao.taxonomia import Taxonomia, carregar_taxonomia

RAIZ = Path(__file__).resolve().parent.parent
PROPOSTA = RAIZ / "config" / "taxonomia.proposta.yaml"
VERSIONADA = RAIZ / "config" / "taxonomia.yaml"
FIXTURE = Path(__file__).resolve().parent / "fixtures" / "infodesk_referencia.json"
SINTETICO = RAIZ / "evals" / "classificacao" / "sintetico.jsonl"
CONVERSA = RAIZ / "evals" / "conversa"
PENDENTES = CONVERSA / "pendentes"
DOC_DECISOES = RAIZ.parent / "docs" / "plano" / "decisoes" / "taxonomia-proposta.md"

MESA_UNICA = "Abertura de Ticket"  # decisão do dono do produto, 2026-10-22
MARCA_DEPARTAMENTO = "# DECISÃO PENDENTE (P11/D1)"


def _referencia() -> dict[str, Any]:
    return json.loads(FIXTURE.read_text(encoding="utf-8"))


def _mesa(ref: dict[str, Any], nome: str) -> dict[str, Any]:
    achadas = [m for m in ref["mesas"] if m["nome"] == nome]
    assert len(achadas) == 1, f"mesa {nome!r} deve existir uma vez na fixture"
    return achadas[0]


@pytest.fixture(scope="module")
def tax() -> Taxonomia:
    return carregar_taxonomia(PROPOSTA)


@pytest.fixture(scope="module")
def ref() -> dict[str, Any]:
    return _referencia()


# --------------------------------------------------------------------------- fixture


def test_fixture_so_tem_nomes_e_ids(ref: dict[str, Any]) -> None:
    """Nada de pessoa, contato ou chave: só os campos de referência esperados."""
    assert set(ref) == {"_comentario", "coletado_em", "mesas", "departamentos"}
    assert all(set(d) == {"id", "nome"} for d in ref["departamentos"])
    permitido = {
        "id",
        "nome",
        "catalogo_habilitado",
        "prioridades",
        "itens_catalogo",
        "qtd_itens_catalogo",
    }
    for m in ref["mesas"]:
        assert set(m) <= permitido, f"campo inesperado em {m['nome']}: {set(m) - permitido}"
        for p in m["prioridades"]:
            assert set(p) == {"id", "nome"}
        for item in m.get("itens_catalogo", []):
            assert set(item) == {"id", "nome", "area", "tipo"}
    bruto = FIXTURE.read_text(encoding="utf-8")
    assert not re.search(r"@|\+55|\b\d{11}\b|\bik_|whsec_", bruto), "possível dado pessoal/segredo"


def test_fixture_cobre_a_mesa_unica(ref: dict[str, Any]) -> None:
    mesa = _mesa(ref, MESA_UNICA)
    assert [p["nome"] for p in mesa["prioridades"]] == ["Alta", "Baixa"]  # fato do inventário
    itens = mesa["itens_catalogo"]
    assert len(itens) == 399
    assert len({i["id"] for i in itens}) == 399
    assert {d["nome"] for d in ref["departamentos"]} == {
        "Administrativo / Financeiro",
        "Comercial",
        "Técnicos",
    }


# --------------------------------------------------------------------------- formato


def test_proposta_carrega_e_continua_placeholder(tax: Taxonomia) -> None:
    assert tax.placeholder is True  # proposta à espera de decisão: nunca remover por conta própria
    assert tax.versao.endswith("proposta")
    assert "indefinida" in tax.categorias
    texto = PROPOSTA.read_text(encoding="utf-8")
    assert "PROPOSTA DE TAXONOMIA" in texto.splitlines()[0]
    assert "À ESPERA DE DECISÃO" in texto.splitlines()[0]


def test_nao_substitui_a_taxonomia_em_uso() -> None:
    """A proposta é um arquivo à parte: o versionado segue o placeholder do E2.2."""
    assert carregar_taxonomia(VERSIONADA).placeholder is True
    assert set(carregar_taxonomia(VERSIONADA).nomes) != set(carregar_taxonomia(PROPOSTA).nomes)


def test_toda_categoria_tem_descricao_exemplos_e_nao_e(tax: Taxonomia) -> None:
    for nome, cat in tax.categorias.items():
        assert cat.exemplos, f"{nome}: sem exemplos"
        if nome != "indefinida":
            assert cat.nao_e, f"{nome}: sem nao_e"
        assert tax.criterios_jev()[nome]["what"] == cat.descricao


def test_exemplos_sem_dado_pessoal(tax: Taxonomia) -> None:
    for nome, cat in tax.categorias.items():
        for frase in (*cat.exemplos, *cat.nao_e, cat.descricao):
            assert not re.search(r"@|\+?\d{8,}|\bcpf\b|\d{3}\.\d{3}\.\d{3}", frase, re.I), (
                nome,
                frase,
            )


# ------------------------------------------------------------ categorias = áreas do catálogo

#: Decisão do dono do produto (2026-10-22): as categorias são as ÁREAS do catálogo do Infodesk,
#: sem juntar, separar nem inventar. Categoria da proposta -> nome EXATO da área em "Abertura de Ticket".
AREA_DO_CATALOGO: dict[str, str] = {
    "microsoft_365": "Microsoft 365",
    "google_workspace": "Google Workspace",
    "firewall": "Firewall",
    "redes": "Redes",
    "servidores": "Servidores",
    "backup": "Backup",
    "seguranca": "Segurança",
    "computadores": "Computadores",
    "impressoras": "Impressoras",
    "telefonia": "Telefonia",
    "softwares": "Softwares",
    "monitoramento": "Monitoramento",
    "cloud": "Cloud",
    "banco_de_dados": "Banco de Dados",
    "certificados_digitais": "Certificados Digitais",
    "projetos": "Projetos",
    "consultoria": "Consultoria",
}


def test_categorias_sao_exatamente_as_areas_do_catalogo(
    tax: Taxonomia, ref: dict[str, Any]
) -> None:
    areas = {i["area"] for i in _mesa(ref, MESA_UNICA)["itens_catalogo"]}
    assert len(areas) == 17
    assert set(AREA_DO_CATALOGO.values()) == areas, "área do Infodesk sem categoria (ou inventada)"
    assert set(tax.categorias) == set(AREA_DO_CATALOGO) | {"indefinida"}


def test_comentario_de_area_de_cada_categoria_confere() -> None:
    """O YAML diz, em cada categoria, de qual área ela é; tem de bater com o mapeamento."""
    atual = None
    vistas: dict[str, str] = {}
    for linha in PROPOSTA.read_text(encoding="utf-8").splitlines():
        if m := re.match(r"^  ([a-z0-9_]+):\s*$", linha):
            atual = m.group(1)
        elif atual and (m := re.match(r"\s+# Área do catálogo: ([^(]+?)\s*(\(|$)", linha)):
            vistas[atual] = m.group(1)
    assert vistas == AREA_DO_CATALOGO


# --------------------------------------------------------------- mesa, prioridade, catálogo


def test_toda_categoria_vai_para_a_mesa_unica(tax: Taxonomia, ref: dict[str, Any]) -> None:
    assert tax.mesa_triagem == MESA_UNICA
    _mesa(ref, MESA_UNICA)
    for nome, cat in tax.categorias.items():
        assert cat.mesa == MESA_UNICA, (
            f"{nome}: mesa {cat.mesa!r} (decisão de 2026-10-22: mesa única)"
        )


def test_prioridades_existem_na_mesa_pelo_nome_exato(tax: Taxonomia, ref: dict[str, Any]) -> None:
    nomes_da_mesa = {p["nome"] for p in _mesa(ref, MESA_UNICA)["prioridades"]}
    for canonica, nome in tax.prioridades.items():
        if nome is not None:
            assert nome in nomes_da_mesa, f"{canonica} -> {nome!r} não existe em {MESA_UNICA!r}"
    # A mesa só tem Alta e Baixa: o que não casa fica explícito (null), nunca um nome inventado.
    assert {c for c, n in tax.prioridades.items() if n is None} == {"media"}
    assert tax.prioridades["baixa"] == "Baixa"
    assert tax.prioridades["alta"] == "Alta"


def test_piso_e_padrao_so_apontam_para_prioridade_que_existe(tax: Taxonomia) -> None:
    for nome, cat in tax.categorias.items():
        for campo in ("prioridade", "prioridade_minima"):
            valor = getattr(cat, campo)
            if valor is not None:
                assert tax.prioridades[valor] is not None, (
                    f"{nome}.{campo}={valor}: sem nome na mesa, o priorityId seria omitido"
                )


# Hoje a proposta omite `catalogo` em todas as categorias (Q4): este teste vale quando alguém
# preencher um item; até lá ele não tem o que conferir de propósito.
def test_catalogo_so_quando_inequivoco(tax: Taxonomia, ref: dict[str, Any]) -> None:
    itens = _mesa(ref, MESA_UNICA)["itens_catalogo"]
    for nome, cat in tax.categorias.items():
        if cat.catalogo is None:
            continue
        achados = [i for i in itens if i["nome"] == cat.catalogo]
        assert len(achados) == 1, (
            f"{nome}: {cat.catalogo!r} deve existir uma vez no catálogo da mesa"
        )


# --------------------------------------------------------------------------- departamento


def test_departamentos_existem_pelo_nome_exato(tax: Taxonomia, ref: dict[str, Any]) -> None:
    existentes = {d["nome"] for d in ref["departamentos"]}
    assert tax.departamento_humano_padrao in existentes
    for nome, cat in tax.categorias.items():
        assert cat.departamento in existentes, f"{nome}: departamento {cat.departamento!r}"


def test_decisoes_pendentes_estao_marcadas_no_arquivo() -> None:
    linhas = PROPOSTA.read_text(encoding="utf-8").splitlines()
    departamentos = [
        x
        for x in linhas
        if re.match(r"\s+departamento:", x) or x.startswith("departamento_humano_padrao:")
    ]
    assert len(departamentos) == len(carregar_taxonomia(PROPOSTA).categorias) + 1
    for linha in departamentos:
        assert MARCA_DEPARTAMENTO in linha, (
            f"departamento sem marca de decisão pendente: {linha.strip()}"
        )
    for linha in linhas:
        if re.match(r"\s+(media|urgente): ", linha):
            assert "DECISÃO PENDENTE" in linha, linha.strip()


# ------------------------------------------------------------- o sintético de classificação

#: Categoria do `evals/classificacao/sintetico.jsonl` (escrito para o placeholder de provedor de
#: internet) -> categoria da proposta (área do catálogo). `indefinida` = o catálogo do Infodesk não
#: tem área para o assunto (financeiro, comercial, serviços de provedor); nunca vira categoria inventada.
DESTINO_SINTETICO: dict[str, tuple[str, str]] = {
    "sem_conexao": ("redes", "área Redes: Sem internet"),
    "lentidao": ("redes", "área Redes: Internet lenta"),
    "instabilidade": ("redes", "área Redes: Perda de pacotes, Link secundário"),
    "equipamento": ("redes", "o sintético fala de modem/roteador/cabo: equipamento de rede"),
    "cobranca": ("indefinida", "sem área financeira no catálogo do Infodesk (Q6)"),
    "segunda_via": ("indefinida", "sem área financeira no catálogo do Infodesk (Q6)"),
    "duvida_planos": ("indefinida", "sem área comercial de planos no catálogo do Infodesk (Q6)"),
    "cancelamento": ("indefinida", "sem área no catálogo; legado de provedor (Q6)"),
    "mudanca_endereco": ("indefinida", "sem área no catálogo; legado de provedor (Q6)"),
    "visita_tecnica": ("indefinida", "sem área no catálogo; legado de provedor (Q6)"),
    "indefinida": ("indefinida", "continua indefinida"),
}


def test_nenhuma_categoria_do_sintetico_ficou_sem_destino(tax: Taxonomia) -> None:
    usadas = {
        json.loads(linha)["esperado"]["categoria"]
        for linha in SINTETICO.read_text(encoding="utf-8").splitlines()
        if linha.strip()
    }
    assert usadas == set(DESTINO_SINTETICO), (
        f"sem destino: {sorted(usadas - set(DESTINO_SINTETICO))}; "
        f"destino órfão: {sorted(set(DESTINO_SINTETICO) - usadas)}"
    )
    for velha, (nova, motivo) in DESTINO_SINTETICO.items():
        assert tax.existe(nova), f"{velha} -> {nova}: categoria inexistente na proposta"
        assert motivo


# ------------------------------------------------------------------ o documento de decisões


def test_documento_de_decisoes_cobre_cada_categoria_e_pergunta(tax: Taxonomia) -> None:
    texto = DOC_DECISOES.read_text(encoding="utf-8")
    for nome in tax.categorias:
        assert f"`{nome}`" in texto, f"documento de decisões não cita a categoria {nome}"
    for q in ("Q1", "Q2", "Q3", "Q4", "Q5", "Q6"):
        assert re.search(rf"^#+ {q}\b", texto, re.M), f"falta a seção da pergunta {q}"


# --------------------------------------------------------- cenários de conversa contra a proposta


@pytest.fixture
def com_a_proposta(monkeypatch: pytest.MonkeyPatch) -> Iterator[None]:
    """O cérebro lê `TAXONOMIA_PATH`: aponta para a proposta só neste teste."""
    monkeypatch.setenv("TAXONOMIA_PATH", str(PROPOSTA))
    get_config_classificacao.cache_clear()
    try:
        yield
    finally:
        get_config_classificacao.cache_clear()


#: Cenários cuja categoria o catálogo do Infodesk não tem (Q6): não rodam contra a proposta; ficam
#: em `pendentes/` com a pergunta, sem perder nenhum assert.
SEM_AREA_NO_CATALOGO = frozenset({"real-cobranca-financeiro", "real-financeiro-segunda-via"})


def _arquivos_reais() -> list[Path]:
    return sorted(CONVERSA.glob("real-*.yaml")) + sorted(PENDENTES.glob("real-*.yaml"))


def _cenarios_de_origem_real() -> list[Any]:
    categorias = carregar_taxonomia_evals(PROPOSTA).categorias
    arquivos = [p for p in _arquivos_reais() if p.stem not in SEM_AREA_NO_CATALOGO]
    cenarios = [carregar_cenario_eval(p, categorias) for p in arquivos]
    assert len({c.id for c in cenarios}) == len(cenarios)
    return cenarios


_CENARIOS = _cenarios_de_origem_real()


def test_ha_32_cenarios_de_origem_real() -> None:
    assert len(_arquivos_reais()) == 32  # nenhum se perde na promoção (git mv) nem na remapeação
    assert len(_CENARIOS) == 30


def test_cenarios_sem_area_usam_categoria_que_a_proposta_nao_tem(tax: Taxonomia) -> None:
    for nome in SEM_AREA_NO_CATALOGO:
        texto = (PENDENTES / f"{nome}.yaml").read_text(encoding="utf-8")
        usadas = set(re.findall(r"categoria: \[?(\w+)", texto))
        assert usadas - {"indefinida"}, nome
        assert not any(tax.existe(c) for c in usadas - {"indefinida"}), (nome, usadas)
        assert re.search(r"^# Destrava: Q6\b", texto, re.M), nome


def _param(cenario: Any) -> Any:
    marcas = [pytest.mark.xfail(reason=cenario.xfail, strict=True)] if cenario.xfail else []
    return pytest.param(cenario, id=cenario.arquivo.stem, marks=marcas)


@pytest.mark.parametrize("cenario", [_param(c) for c in _CENARIOS])
def test_cenario_real_roda_contra_a_proposta(cenario: Any, com_a_proposta: None) -> None:
    resultado = runner.executar(cenario)
    assert resultado.passou, "\n".join(resultado.falhas)


def test_todo_pendente_diz_qual_decisao_o_destrava() -> None:
    arquivos = sorted(PENDENTES.glob("real-*.yaml"))
    assert arquivos
    for arquivo in arquivos:
        texto = arquivo.read_text(encoding="utf-8")
        assert re.search(r"^# Destrava: .*\bQ[2-6]\b", texto, re.M), (
            f"{arquivo.name}: sem a linha '# Destrava: … Q<n> …'"
        )


def test_promovidos_sao_validos_tambem_na_taxonomia_em_uso() -> None:
    """O que está na raiz roda no CI de hoje (placeholder) e na proposta."""
    categorias = carregar_taxonomia_evals(VERSIONADA).categorias
    for arquivo in sorted(CONVERSA.glob("real-*.yaml")):
        carregar_cenario_eval(arquivo, categorias)
