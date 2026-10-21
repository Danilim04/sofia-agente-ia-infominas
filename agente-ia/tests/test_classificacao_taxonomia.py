"""Taxonomia (`config/taxonomia.yaml`): carga, validação, `criteria` do JEV e o
aviso de placeholder (P1) — E2.2.
"""

from __future__ import annotations

import copy
import logging
from pathlib import Path

import pytest
import yaml

from src.classificacao.taxonomia import (
    ErroTaxonomia,
    carregar_taxonomia,
    validar_taxonomia,
)
from tests.fakes import config_de_teste, taxonomia_de_teste

CAMINHO = config_de_teste().taxonomia_path


def _bruto() -> dict:
    return copy.deepcopy(yaml.safe_load(CAMINHO.read_text(encoding="utf-8")))


def _salvar(tmp_path: Path, dado: dict) -> Path:
    arquivo = tmp_path / "taxonomia.yaml"
    arquivo.write_text(yaml.safe_dump(dado, allow_unicode=True), encoding="utf-8")
    return arquivo


def test_carrega_a_taxonomia_versionada():
    tax = taxonomia_de_teste()
    assert tax.placeholder is True  # P1: enquanto o export do Infodesk não vem
    assert "indefinida" in tax.categorias
    assert {"sem_conexao", "lentidao", "cobranca"} <= set(tax.nomes)
    assert tax.prioridade_minima("sem_conexao") == "alta"
    assert tax.prioridade_minima("lentidao") is None
    assert tax.mesa_triagem and tax.departamento_humano_padrao


def test_boot_avisa_enquanto_for_placeholder(caplog):
    with caplog.at_level(logging.WARNING, logger="src.classificacao.taxonomia"):
        carregar_taxonomia(CAMINHO)
    assert "taxonomia_placeholder" in caplog.text
    assert "P1" in caplog.text


def test_sem_placeholder_nao_avisa(tmp_path, caplog):
    dado = _bruto()
    dado["placeholder"] = False
    with caplog.at_level(logging.WARNING, logger="src.classificacao.taxonomia"):
        tax = carregar_taxonomia(_salvar(tmp_path, dado))
    assert tax.placeholder is False
    assert "taxonomia_placeholder" not in caplog.text


def test_criterios_jev_usam_o_objeto_estruturado_da_doc():
    criterios = taxonomia_de_teste().criterios_jev()
    sem_conexao = criterios["sem_conexao"]
    assert set(sem_conexao) == {"what", "not_for", "examples"}
    assert "Internet totalmente sem funcionar" in sem_conexao["what"]
    assert "minha internet caiu" in sem_conexao["examples"]
    assert "lentidão com conexão ativa" in sem_conexao["not_for"]
    # `indefinida` também vira opção do Choice ("use 'indefinida' se não há informação")
    assert criterios["indefinida"]["what"]
    assert "not_for" not in criterios["indefinida"]  # `nao_e: []` não gera chave vazia


def test_mesa_e_departamento_ficam_fora_dos_criterios_do_jev():
    texto = str(taxonomia_de_teste().criterios_jev())
    assert "Suporte Técnico" not in texto and "Financeiro" not in texto


def test_arquivo_inexistente():
    with pytest.raises(ErroTaxonomia, match="não foi possível ler"):
        carregar_taxonomia("/nao/existe/taxonomia.yaml")


def test_yaml_invalido(tmp_path):
    arquivo = tmp_path / "t.yaml"
    arquivo.write_text("categorias: [\n", encoding="utf-8")
    with pytest.raises(ErroTaxonomia, match="YAML inválido"):
        carregar_taxonomia(arquivo)


def test_chave_duplicada_e_recusada(tmp_path):
    arquivo = tmp_path / "t.yaml"
    texto = CAMINHO.read_text(encoding="utf-8").replace(
        "  lentidao:\n", "  sem_conexao:\n    descricao: x\n  lentidao:\n", 1
    )
    arquivo.write_text(texto, encoding="utf-8")
    with pytest.raises(ErroTaxonomia, match="chave duplicada"):
        carregar_taxonomia(arquivo)


def test_exige_a_categoria_indefinida():
    dado = _bruto()
    del dado["categorias"]["indefinida"]
    with pytest.raises(ErroTaxonomia, match="indefinida"):
        validar_taxonomia(dado)


@pytest.mark.parametrize("campo", ["descricao", "mesa", "departamento"])
def test_campos_obrigatorios_da_categoria(campo):
    dado = _bruto()
    del dado["categorias"]["lentidao"][campo]
    with pytest.raises(ErroTaxonomia, match=f"categorias.lentidao.*{campo}"):
        validar_taxonomia(dado)


def test_catalogo_e_opcional():
    dado = _bruto()
    assert "catalogo" not in dado["categorias"]["lentidao"]
    assert validar_taxonomia(dado).categorias["lentidao"].catalogo is None
    dado["categorias"]["lentidao"]["catalogo"] = "Serviços de Suporte"
    assert validar_taxonomia(dado).categorias["lentidao"].catalogo == "Serviços de Suporte"


def test_prioridade_da_categoria_deve_existir_em_prioridades():
    dado = _bruto()
    dado["categorias"]["lentidao"]["prioridade"] = "critica"
    with pytest.raises(ErroTaxonomia, match="prioridade"):
        validar_taxonomia(dado)
    dado = _bruto()
    dado["categorias"]["lentidao"]["prioridade_minima"] = "critica"
    with pytest.raises(ErroTaxonomia, match="prioridade_minima"):
        validar_taxonomia(dado)


def test_prioridades_canonicas_sao_exatamente_as_quatro_do_score_do_jev():
    dado = _bruto()
    del dado["prioridades"]["urgente"]
    with pytest.raises(ErroTaxonomia, match="prioridades"):
        validar_taxonomia(dado)
    dado = _bruto()
    dado["prioridades"]["critica"] = None
    with pytest.raises(ErroTaxonomia, match="prioridades"):
        validar_taxonomia(dado)


@pytest.mark.parametrize("nome", ["Sem Conexão", "semConexao", "1abc", "com-hifen"])
def test_nome_de_categoria_snake_case_sem_acento(nome):
    dado = _bruto()
    dado["categorias"][nome] = dict(dado["categorias"]["lentidao"])
    with pytest.raises(ErroTaxonomia, match="snake_case"):
        validar_taxonomia(dado)


def test_exemplos_devem_ser_lista_de_textos():
    dado = _bruto()
    dado["categorias"]["lentidao"]["exemplos"] = "uma string"
    with pytest.raises(ErroTaxonomia, match="lista de textos"):
        validar_taxonomia(dado)


def test_topo_exige_mesa_triagem_e_departamento_padrao():
    for chave in ("mesa_triagem", "departamento_humano_padrao"):
        dado = _bruto()
        del dado[chave]
        with pytest.raises(ErroTaxonomia, match=chave):
            validar_taxonomia(dado)


def test_taxonomia_path_vem_do_ambiente(monkeypatch, tmp_path):
    arquivo = _salvar(tmp_path, _bruto())
    monkeypatch.setenv("TAXONOMIA_PATH", str(arquivo))
    assert config_de_teste().taxonomia_path == arquivo
