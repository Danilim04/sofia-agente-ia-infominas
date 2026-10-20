"""Validação dos formatos: cada erro aponta campo e motivo."""

from __future__ import annotations

import copy
from pathlib import Path
from typing import Any

import pytest
import yaml

from evals import formatos
from evals.formatos import ErroFormato

CATEGORIAS = frozenset({"sem_conexao", "lentidao", "cobranca", "indefinida"})

CASO_OK: dict[str, Any] = {
    "id": "cls-0001",
    "fonte": "sintetico",
    "estado": ["Cliente: a net caiu"],
    "esperado": {
        "intencao": "reclamacao_problema",
        "categoria": "sem_conexao",
        "prioridade_min": "alta",
        "frustracao_max": 1,
    },
    "notas": "simples",
}


def _erro(fn, dado, **kw) -> ErroFormato:
    with pytest.raises(ErroFormato) as exc:
        fn(dado, **kw)
    return exc.value


# ------------------------------------------------------------------ caso
def test_caso_valido_vira_dataclass():
    caso = formatos.validar_caso(CASO_OK, categorias=CATEGORIAS)
    assert caso.id == "cls-0001"
    assert caso.estado == ("Cliente: a net caiu",)
    assert caso.esperado.prioridade_min == "alta"
    assert caso.esperado.frustracao_max == 1
    assert caso.rotulador is None


def test_caso_minimo_sem_opcionais():
    dado = {
        "id": "x",
        "fonte": "sintetico",
        "estado": ["oi"],
        "esperado": {"intencao": "outro", "categoria": "indefinida"},
    }
    caso = formatos.validar_caso(dado)
    assert caso.esperado.prioridade_min is None and caso.notas is None


@pytest.mark.parametrize(
    ("mutacao", "campo", "trecho"),
    [
        (lambda d: d.pop("id"), "caso.id", "texto"),
        (lambda d: d.__setitem__("fonte", "chute"), "caso.fonte", "sintetico"),
        (lambda d: d.__setitem__("estado", []), "caso.estado", "vazia"),
        (lambda d: d.__setitem__("estado", ["ok", 3]), "caso.estado[1]", "texto"),
        (lambda d: d["esperado"].pop("intencao"), "caso.esperado.intencao", "texto"),
        (
            lambda d: d["esperado"].__setitem__("intencao", "reclamar"),
            "caso.esperado.intencao",
            "não está em",
        ),
        (
            lambda d: d["esperado"].__setitem__("categoria", "wifi"),
            "caso.esperado.categoria",
            "não existe na taxonomia",
        ),
        (
            lambda d: d["esperado"].__setitem__("prioridade_min", "altissima"),
            "caso.esperado.prioridade_min",
            "baixa",
        ),
        (
            lambda d: d["esperado"].__setitem__("frustracao_max", 5),
            "caso.esperado.frustracao_max",
            "entre 0 e 3",
        ),
        (
            lambda d: d["esperado"].__setitem__("frustracao_max", True),
            "caso.esperado.frustracao_max",
            "inteiro",
        ),
        (
            lambda d: d["esperado"].__setitem__("prioridade", "alta"),
            "caso.esperado.prioridade",
            "desconhecida",
        ),
        (lambda d: d.__setitem__("extra", 1), "caso.extra", "desconhecida"),
        (lambda d: d.__setitem__("fonte", "historico"), "caso.rotulador", "obrigatório"),
    ],
)
def test_caso_invalido_aponta_campo(mutacao, campo, trecho):
    dado = copy.deepcopy(CASO_OK)
    mutacao(dado)
    erro = _erro(formatos.validar_caso, dado, categorias=CATEGORIAS)
    assert erro.campo == campo
    assert trecho in erro.motivo


def test_caso_com_pisos_de_frustracao_e_nouls():
    dado = copy.deepcopy(CASO_OK)
    dado["esperado"].update({"frustracao_min": 1, "malicioso_min": 0.8, "fora_de_escopo_min": 0})
    e = formatos.validar_caso(dado, categorias=CATEGORIAS).esperado
    assert (e.frustracao_min, e.frustracao_max, e.malicioso_min, e.fora_de_escopo_min) == (
        1,
        1,
        0.8,
        0.0,
    )


@pytest.mark.parametrize(
    ("chave", "valor", "trecho"),
    [
        ("frustracao_min", 4, "entre 0 e 3"),
        ("frustracao_min", 2, "maior que frustracao_max"),  # CASO_OK tem frustracao_max 1
        ("malicioso_min", 1.5, "entre 0 e 1"),
        ("fora_de_escopo_min", "alto", "entre 0 e 1"),
    ],
)
def test_pisos_invalidos_apontam_campo(chave, valor, trecho):
    dado = copy.deepcopy(CASO_OK)
    dado["esperado"][chave] = valor
    erro = _erro(formatos.validar_caso, dado, categorias=CATEGORIAS)
    assert erro.campo == f"caso.esperado.{chave}"
    assert trecho in erro.motivo


def test_caso_historico_exige_rotulador_e_aceita():
    dado = {**copy.deepcopy(CASO_OK), "fonte": "historico", "rotulador": "infominas-ana"}
    assert formatos.validar_caso(dado).rotulador == "infominas-ana"


def test_casos_rejeita_id_repetido():
    erro = _erro(formatos.validar_casos, [CASO_OK, copy.deepcopy(CASO_OK)])
    assert erro.campo == "casos[1].id"
    assert "repetido" in erro.motivo


def test_casos_prefixa_indice_no_campo():
    ruim = copy.deepcopy(CASO_OK)
    ruim["esperado"]["intencao"] = "zzz"
    erro = _erro(formatos.validar_casos, [CASO_OK, ruim])
    assert erro.campo == "casos[1].esperado.intencao"


# --------------------------------------------------------------- taxonomia
def test_taxonomia_real_carrega():
    tax = formatos.carregar_taxonomia()
    assert tax.placeholder is True
    assert "indefinida" in tax.categorias
    assert len(tax.categorias) >= 11
    assert tax.prioridade_minima["sem_conexao"] == "alta"


def test_taxonomia_sem_indefinida_falha(tmp_path: Path):
    arq = tmp_path / "t.yaml"
    arq.write_text(
        yaml.safe_dump({"categorias": {"x": {"descricao": "d", "mesa": "m", "departamento": "d"}}}),
        encoding="utf-8",
    )
    erro = _erro(formatos.carregar_taxonomia, arq)
    assert erro.campo == "taxonomia.categorias"
    assert "indefinida" in erro.motivo


def test_taxonomia_mesa_obrigatoria(tmp_path: Path):
    arq = tmp_path / "t.yaml"
    arq.write_text(
        yaml.safe_dump({"categorias": {"indefinida": {"descricao": "d", "departamento": "d"}}}),
        encoding="utf-8",
    )
    assert _erro(formatos.carregar_taxonomia, arq).campo == "taxonomia.categorias.indefinida.mesa"


# ---------------------------------------------------------------- cenário
def _cenario_ok() -> dict[str, Any]:
    jev = {
        "intencao": ["reclamacao_problema", 0.9],
        "categoria": ["sem_conexao", 0.8],
        "prioridade": [2, 0.7],
        "frustracao": [1, 0.8],
        "malicioso": 0.01,
        "fora_de_escopo": 0.01,
    }
    return {
        "id": "conversa-900",
        "descricao": "teste",
        "sessao": {"telefone": "+5531999990000", "nome_contato": "X", "contato": None},
        "jev": [jev, copy.deepcopy(jev)],
        "turnos": [
            {
                "cliente": "caiu",
                "llm": [
                    {"tool": "preparar_abertura_chamado", "args": {"titulo": "t"}},
                    {"texto": "Posso abrir?"},
                ],
                "espera": {"chama": ["preparar_abertura_chamado"], "nao_chama": ["abrir_chamado"]},
            },
            {
                "cliente": "sim",
                "llm": [{"tool": "abrir_chamado", "args": {}}, {"texto": "Pronto, 4821"}],
                "espera": {"chama": ["abrir_chamado"], "contem": ["4821"], "bolhas": 1},
            },
        ],
    }


def test_cenario_valido():
    c = formatos.validar_cenario(_cenario_ok(), categorias=CATEGORIAS)
    assert c.id == "conversa-900"
    assert len(c.jev) == len(c.turnos) == 2
    assert c.jev[0].intencao == ("reclamacao_problema", 0.9)
    assert c.turnos[0].llm[0].tool == "preparar_abertura_chamado"
    assert c.turnos[0].llm[-1].texto == "Posso abrir?"
    assert c.turnos[1].espera["bolhas"] == 1


@pytest.mark.parametrize(
    ("mutacao", "campo", "trecho"),
    [
        (lambda d: d["jev"].pop(), "cenario.jev", "uma por turno"),
        (lambda d: d["jev"][0].pop("malicioso"), "cenario.jev[0].malicioso", "ausente"),
        (
            lambda d: d["jev"][0].__setitem__("malicioso", 1.5),
            "cenario.jev[0].malicioso",
            "entre 0 e 1",
        ),
        (
            lambda d: d["jev"][1].__setitem__("intencao", ["xpto", 0.9]),
            "cenario.jev[1].intencao[0]",
            "não está em",
        ),
        (
            lambda d: d["jev"][1].__setitem__("categoria", ["wifi", 0.9]),
            "cenario.jev[1].categoria[0]",
            "não está em",
        ),
        (
            lambda d: d["jev"][0].__setitem__("prioridade", [7, 0.9]),
            "cenario.jev[0].prioridade[0]",
            "entre 0 e 3",
        ),
        (lambda d: d["turnos"][0]["llm"].pop(), "cenario.turnos[0].llm[0]", "último passo"),
        (
            lambda d: d["turnos"][0]["llm"][0].__setitem__("texto", "x"),
            "cenario.turnos[0].llm[0]",
            "exatamente um",
        ),
        (
            lambda d: d["turnos"][0]["llm"][1].__setitem__("args", {}),
            "cenario.turnos[0].llm[1]",
            "só valem com 'tool'",
        ),
        (
            lambda d: d["turnos"][0]["espera"].__setitem__("chama", ["abrir_chamado"]),
            "cenario.turnos[0].espera.nao_chama",
            "também está em",
        ),
        (
            lambda d: d["turnos"][1].__setitem__("espera", {}),
            "cenario.turnos[1].espera",
            "observável",
        ),
        (
            lambda d: d["turnos"][1]["espera"].__setitem__("resposta", "x"),
            "cenario.turnos[1].espera.resposta",
            "desconhecida",
        ),
        (
            lambda d: d["turnos"][1]["espera"].__setitem__("bolhas", "duas"),
            "cenario.turnos[1].espera.bolhas",
            "inteiro",
        ),
        (lambda d: d["sessao"].pop("telefone"), "cenario.sessao.telefone", "texto"),
        (lambda d: d.__setitem__("identidade", {}), "cenario.identidade", "desconhecida"),
    ],
)
def test_cenario_invalido_aponta_campo(mutacao, campo, trecho):
    dado = _cenario_ok()
    mutacao(dado)
    erro = _erro(formatos.validar_cenario, dado, categorias=CATEGORIAS)
    assert erro.campo == campo, erro
    assert trecho in erro.motivo


def test_carregar_cenario_usa_nome_do_arquivo_no_campo(tmp_path: Path):
    arq = tmp_path / "quebrado.yaml"
    dado = _cenario_ok()
    dado["turnos"][0]["espera"] = {}
    arq.write_text(yaml.safe_dump(dado, allow_unicode=True), encoding="utf-8")
    erro = _erro(formatos.carregar_cenario, arq)
    assert erro.campo == "quebrado.yaml.turnos[0].espera"


def test_ordem_prioridade():
    assert [formatos.ordem_prioridade(p) for p in formatos.PRIORIDADES] == [0, 1, 2, 3]
