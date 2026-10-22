"""Parser da resposta do juiz — casos escritos à mão, sem rede."""

from __future__ import annotations

import json

import pytest

from evals.juiz.parser import MAX_JUSTIFICATIVA, ErroJuiz, parse_resposta_juiz

pytestmark = pytest.mark.evals_pr

CRITERIOS = ("persona", "exatidao")


def _ok(persona: str = "4", exatidao: str = "5") -> str:
    """Resposta do juiz; `persona`/`exatidao` entram como texto JSON cru (para testar tipos errados)."""
    return (
        '{"criterios": {"persona": {"nota": ' + persona + ', "justificativa": "tom bom"},'
        ' "exatidao": {"nota": ' + exatidao + ', "justificativa": "bate com a ferramenta"}}}'
    )


def test_resposta_limpa():
    j = parse_resposta_juiz(_ok(), CRITERIOS)
    assert j.notas["persona"].nota == 4 and j.notas["persona"].justificativa == "tom bom"
    assert j.notas["exatidao"].nota == 5
    # escala 0–100 = nota/5·100: 4 → 80, 5 → 100
    assert j.notas["persona"].pontos == pytest.approx(80.0)
    assert j.notas["exatidao"].pontos == pytest.approx(100.0)


def test_bloco_de_codigo_e_texto_em_volta():
    texto = "Claro! Segue a avaliação:\n```json\n" + _ok("3", "2") + "\n```\nEspero ter ajudado."
    j = parse_resposta_juiz(texto, CRITERIOS)
    assert (j.notas["persona"].nota, j.notas["exatidao"].nota) == (3, 2)


def test_texto_com_chaves_soltas_antes_do_objeto():
    j = parse_resposta_juiz("pensando {assim} e {isso:} depois: " + _ok(), CRITERIOS)
    assert j.notas["persona"].nota == 4


@pytest.mark.parametrize("valor", ["4.0", '"4"', '"4/5"', '"4,0"'])
def test_nota_em_formatos_toleraveis(valor: str):
    assert parse_resposta_juiz(_ok(valor), CRITERIOS).notas["persona"].nota == 4


def test_forma_curta_sem_justificativa():
    j = parse_resposta_juiz('{"persona": 5, "exatidao": 0}', CRITERIOS)
    assert (j.notas["persona"].nota, j.notas["exatidao"].nota) == (5, 0)
    assert j.notas["persona"].justificativa == ""


def test_criterio_extra_e_ignorado_e_justificativa_e_cortada():
    longa = "x " * 400
    texto = json.dumps(
        {"criterios": {"persona": {"nota": 4, "justificativa": longa}, "exatidao": 3, "outro": 1}}
    )
    j = parse_resposta_juiz(texto, CRITERIOS)
    assert set(j.notas) == {"persona", "exatidao"}
    assert len(j.notas["persona"].justificativa) <= MAX_JUSTIFICATIVA


@pytest.mark.parametrize(
    ("texto", "trecho"),
    [
        ("não consigo avaliar", "não contém um objeto JSON"),
        ("[1, 2, 3]", "não contém um objeto JSON"),
        ('{"criterios": {"persona": 4}}', "'exatidao' ausente"),
        (_ok("6"), "fora de 0–5"),
        (_ok("-1"), "fora de 0–5"),
        (_ok("3.5"), "não é inteira"),
        (_ok('"bom"'), "não é número"),
        (_ok("true"), "booleana"),
        (_ok("null"), "ausente ou de tipo inválido"),
        ('{"criterios": [1, 2]}', "deve ser um objeto"),
    ],
)
def test_resposta_invalida_levanta(texto: str, trecho: str):
    with pytest.raises(ErroJuiz, match=trecho):
        parse_resposta_juiz(texto, CRITERIOS)


def test_so_o_pedido_e_exigido():
    j = parse_resposta_juiz('{"criterios": {"persona": {"nota": 2}}}', ("persona",))
    assert j.notas["persona"].nota == 2
