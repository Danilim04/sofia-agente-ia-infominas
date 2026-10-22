"""Cenários do juiz: formato, quantidade, rubrica, ausência de PII e versão do prompt."""

from __future__ import annotations

import re
from pathlib import Path

import pytest
import yaml

from evals.anonimizar import Anonimizador
from evals.formatos import ErroFormato
from evals.juiz import prompt as prompt_juiz
from evals.juiz.cenarios import (
    CRITERIOS,
    DIRETORIO_PADRAO,
    MINIMO_CENARIOS,
    carregar_cenarios_juiz,
    selecionar,
    validar_cenario_juiz,
)

pytestmark = pytest.mark.evals_pr

#: Valores fictícios que os cenários determinísticos já usam (CPFs de exemplo e a faixa de
#: telefones de teste `+5531999990xxx`). Qualquer outro dado pessoal derruba o teste.
FICTICIOS = re.compile(
    r"123\.456\.789-09|123\.456\.789-00|529\.982\.247-25|\+5531999990\d{3}|\+55 ?31 ?9\*{4}-\d{4}"
)


@pytest.fixture(scope="module")
def cenarios():
    return carregar_cenarios_juiz()


def test_ha_pelo_menos_20_cenarios_com_ids_unicos(cenarios):
    assert len(cenarios) >= MINIMO_CENARIOS
    ids = [c.id for c in cenarios]
    assert len(ids) == len(set(ids))


def test_rubrica_completa_e_com_negativo(cenarios):
    usados: set[str] = set()
    for c in cenarios:
        r = c.rubrica
        assert set(r.criterios) <= set(CRITERIOS) and r.criterios, c.id
        assert len(r.esperado) >= 30, f"{c.id}: 'esperado' vago demais"
        assert r.nao_deve, f"{c.id}: sem negativo"
        usados |= set(r.criterios)
    assert usados == set(CRITERIOS), (
        "os 4 critérios do plano (persona, exatidão, não inventa, pede confirmação)"
    )


def test_cada_cenario_vem_de_um_deterministico_que_existe(cenarios):
    raiz = DIRETORIO_PADRAO.parent.parent  # evals/
    for c in cenarios:
        assert c.origem and (raiz / c.origem).is_file(), f"{c.id}: origem {c.origem!r} não existe"
        assert c.arquivo is not None and f"{Path(c.origem).stem}" == c.arquivo.stem


def test_pede_confirmacao_so_onde_ha_abertura_de_chamado(cenarios):
    """O critério `pede_confirmacao` só se aplica a quem passa pela abertura do chamado."""
    com_criterio = {c.id for c in cenarios if "pede_confirmacao" in c.rubrica.criterios}
    assert len(com_criterio) >= 3
    for c in cenarios:
        if c.id in com_criterio:
            falas = " ".join(t.cliente.lower() for t in c.base.turnos)
            assert "sim" in falas or "pode abrir" in falas, c.id


def test_cenarios_nao_tem_script_de_llm_nem_asserts():
    bruto = {
        "id": "juiz-999",
        "descricao": "x",
        "sessao": {"telefone": "+5531999990999"},
        "jev": [
            {
                "intencao": ["outro", 0.9],
                "categoria": ["indefinida", 0.9],
                "prioridade": [1, 0.9],
                "frustracao": [0, 0.9],
                "malicioso": 0.0,
                "fora_de_escopo": 0.0,
            }
        ],
        "turnos": [{"cliente": "oi", "llm": [{"texto": "oi"}]}],
        "rubrica": {"criterios": ["persona"], "esperado": "cumprimenta", "nao_deve": ["x"]},
    }
    with pytest.raises(ErroFormato, match="não tem script de LLM"):
        validar_cenario_juiz(bruto, "x.yaml")
    bruto["turnos"] = [{"cliente": "oi"}]
    assert validar_cenario_juiz(bruto, "x.yaml").rubrica.criterios == ("persona",)


@pytest.mark.parametrize(
    ("mudanca", "trecho"),
    [
        ({"criterios": ["persona", "simpatia"]}, "não está em"),
        ({"criterios": []}, "lista não vazia"),
        ({"criterios": ["persona", "persona"]}, "repetido"),
        ({"nao_deve": []}, "todo cenário tem negativo"),
        ({"esperado": " "}, "texto obrigatório"),
        ({"extra": 1}, "chave desconhecida"),
    ],
)
def test_rubrica_invalida_e_recusada(mudanca, trecho):
    bruto = yaml.safe_load(
        (DIRETORIO_PADRAO / "saudacao-nao-aciona-ferramentas.yaml").read_text(encoding="utf-8")
    )
    bruto["rubrica"] = {**bruto["rubrica"], **mudanca}
    with pytest.raises(ErroFormato, match=trecho):
        validar_cenario_juiz(bruto, "x.yaml")


def test_selecionar_por_id_ou_nome_e_erro_para_desconhecido(cenarios):
    assert [c.id for c in selecionar(cenarios, ["juiz-001"])] == ["juiz-001"]
    assert len(selecionar(cenarios, ["saudacao-nao-aciona-ferramentas"])) == 1
    assert len(selecionar(cenarios, [])) == len(cenarios)
    with pytest.raises(ErroFormato, match="não existe"):
        selecionar(cenarios, ["nao-existe"])


def test_cenarios_sem_dado_pessoal():
    """O anonimizador não acha nada além dos valores fictícios conhecidos (CPFs de exemplo, `+5531999990xxx`)."""
    for arquivo in sorted(DIRETORIO_PADRAO.glob("*.yaml")):
        texto = arquivo.read_text(encoding="utf-8")
        sem_ficticios = FICTICIOS.sub("", texto)
        anon = Anonimizador()
        assert anon.texto(sem_ficticios) == sem_ficticios, f"{arquivo.name}: dado pessoal"
        assert "@" not in texto, arquivo.name


# ------------------------------------------------------------------ prompt versionado
def test_prompt_do_juiz_tem_versao_e_hash_em_dia():
    """Mexeu no prompt? Suba VERSAO_PROMPT, atualize SHA256_PROMPT e regrave o baseline no mesmo PR."""
    assert prompt_juiz.hash_prompt() == prompt_juiz.SHA256_PROMPT
    assert re.fullmatch(r"juiz-v\d+", prompt_juiz.VERSAO_PROMPT)


def test_prompt_cobre_os_criterios_a_escala_e_a_defesa_contra_injecao():
    texto = prompt_juiz.ler_prompt()
    for criterio in CRITERIOS:
        assert f"**{criterio}**" in texto
    assert "0 a 5" in texto and "DADO" in texto and "SOMENTE com um objeto JSON" in texto
