"""Cenários de conversa como testes pytest (`pytest -m evals_pr evals/`).

Dois níveis: (1) todo YAML carrega e valida; (2) cada cenário executa no grafo
real (LLM scriptado + `FakeJEV` + gateway falso). Um cenário com `xfail: "motivo"`
descreve o comportamento correto que a Sofia ainda não tem: roda como xfail
ESTRITO — se passar, o teste falha e o marcador deve sair.
"""

from __future__ import annotations

import pytest

from evals.conversa import runner
from evals.formatos import CenarioConversa, ErroFormato, carregar_taxonomia

pytestmark = pytest.mark.evals_pr

# Carga no import para parametrizar; um YAML inválido NÃO derruba a coleção:
# vira falha explícita em test_todos_os_yamls_sao_validos.
_ERRO_CARGA: str | None = None
try:
    _CENARIOS: list[CenarioConversa] = runner.carregar_cenarios(taxonomia=carregar_taxonomia())
except (ErroFormato, ValueError) as _e:
    _ERRO_CARGA = str(_e)
    _CENARIOS = []


def _param(cenario: CenarioConversa) -> object:
    marcas = []
    if cenario.xfail:
        marcas.append(pytest.mark.xfail(reason=cenario.xfail, strict=True))
    return pytest.param(cenario, id=cenario.id, marks=marcas)


def test_todos_os_yamls_sao_validos() -> None:
    assert _ERRO_CARGA is None, _ERRO_CARGA
    assert _CENARIOS, "nenhum cenário em evals/conversa/"
    assert len({c.id for c in _CENARIOS}) == len(_CENARIOS)
    assert any(c.arquivo is not None and c.arquivo.name == "TEMPLATE.yaml" for c in _CENARIOS)


def test_todo_cenario_tem_negativo() -> None:
    """Regra da skill evals-agente: um cenário sempre inclui o que NÃO pode acontecer."""
    for c in _CENARIOS:
        tem_negativo = any(
            "nao_chama" in t.espera or "nao_contem" in t.espera or t.espera.get("bloqueado") is True
            for t in c.turnos
        )
        assert tem_negativo, f"{c.id} não tem assert negativo (nao_chama/nao_contem/bloqueado)"


#: Comportamentos que a linha da E3.5 (docs/plano/03-etapas.md) exige — cada um é o NOME de um
#: cenário (o arquivo é o comportamento provado). Apagar um deles derruba este teste.
COMPORTAMENTOS_DO_PLANO = (
    "abre-ticket-apos-confirmacao",  # reclamação simples até o ticket
    "duvida-respondida-com-rag",
    "status-com-numero",
    "status-de-chamado-de-outra-pessoa-sem-posse",
    "pedido-de-atendente-transfere-por-codigo",
    "audio-pede-texto-e-nao-abre-chamado",
    "cliente-irritado-transfere-sem-prometer-prazo",
    "baixa-confianca-esclarece-depois-categoria",
    "categoria-pegajosa-so-troca-com-confianca-alta",
    "proposta-expirada-nao-abre",
    "esse-nao-confirma-abertura",
    "cpf-invalido-nao-registra",
    "telefone-identificado-dispensa-cpf",
    "ja-tem-chamado-aberto-nao-abre-outro",
    "injecao-direta-regex-bloqueia",
    "injecao-via-rag-nao-vira-comando",
    "fora-de-escopo-redireciona",
    "horario-periodo-do-dia-muda-no-turno",
)
MINIMO_DE_CENARIOS = 30
#: Esperas "de comportamento" (as auxiliares, como `prompt_contem`, somam-se a estas).
ESPERAS_BASICAS = frozenset(
    {"chama", "nao_chama", "transfere", "contem", "nao_contem", "bloqueado"}
)


def _reais() -> list[CenarioConversa]:
    return [c for c in _CENARIOS if c.arquivo is not None and c.arquivo.name != "TEMPLATE.yaml"]


def test_ha_pelo_menos_30_cenarios_que_executam() -> None:
    """E3.5: >= 30 cenários de verdade (o TEMPLATE e os xfail não contam)."""
    executaveis = [c for c in _reais() if not c.xfail]
    assert len(executaveis) >= MINIMO_DE_CENARIOS, len(executaveis)


def test_cobre_os_comportamentos_do_plano() -> None:
    nomes = {c.arquivo.stem for c in _reais() if c.arquivo is not None}
    faltando = [n for n in COMPORTAMENTOS_DO_PLANO if n not in nomes]
    assert not faltando, f"cenários exigidos pela E3.5 ausentes: {faltando}"


def test_todo_turno_tem_espera_de_comportamento() -> None:
    """Cada turno prova algo observável sobre tools, transferência ou texto (não só o prompt)."""
    for c in _reais():
        for i, t in enumerate(c.turnos, start=1):
            assert ESPERAS_BASICAS & set(t.espera), f"{c.id} turno {i}: só esperas auxiliares"


def test_ids_sao_unicos_e_com_prefixo() -> None:
    ids = [c.id for c in _reais()]
    assert len(ids) == len(set(ids))
    assert all(i.startswith("conversa-") for i in ids)


def test_todo_xfail_aponta_o_arquivo_de_produto() -> None:
    """Um xfail é bug de produto: o motivo diz onde (para o PR listar e alguém corrigir)."""
    for c in _reais():
        if c.xfail:
            assert "src/" in c.xfail, f"{c.id}: o motivo do xfail deve citar o arquivo em src/"


@pytest.mark.parametrize("cenario", [_param(c) for c in _CENARIOS])
def test_cenario_executa(cenario: CenarioConversa) -> None:
    resultado = runner.executar(cenario)
    assert resultado.passou, "\n".join(resultado.falhas)
    assert resultado.turnos_executados == len(cenario.turnos)
