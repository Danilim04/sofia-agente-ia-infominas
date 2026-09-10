# Plataforma Web Azapfy: Pacote Gestão da Comprovação

A Azapfy possui duas plataformas principais: a **Mobile** (concebida para uso dos motoristas em campo) e a **Web** (voltada para usuários de escritório, gestores, colaboradores e embarcadores). 

A Plataforma Web é comercializada em módulos. O pacote mais popular e fundamental é o **Gestão da Comprovação**, composto pelos módulos: **Dashboard, Usuários, Romaneios e Pesquisa**. Esse pacote é ideal para quem deseja controlar a saída de documentos, realizar comprovações no ato da entrega e acessar informações logísticas de forma rápida e analítica.

Abaixo, exploramos cada um desses módulos em detalhes.

---

## 1. Módulo: Dashboard

Os dashboards substituem o uso estressante e suscetível a erros de planilhas de Excel. O objetivo é simplificar a visualização de grandes volumes de dados, trazendo uma visão macro, quantitativa e qualitativa do trabalho realizado. A Azapfy oferece duas visões de dashboard:

### 1.1. Dashboard Operacional
É a tela inicial do sistema. Permite aplicar filtros (principalmente por data) e selecionar agrupamentos para direcionar a visão do usuário. A tela é dividida em quadrantes:

* **Comprovações (3 Quadrantes Superiores):** Mostra a relação entre documentos comprovados e abertos. 
    * *Atenção especial para notas em **Auditoria**:* São documentos que foram fotografados pelo motorista, mas o aplicativo não reconheceu automaticamente o número da nota na imagem. Essas notas dependem de aprovação manual de um usuário no sistema.
* **Ocorrências:** Detalha as justificativas (extravio, avaria, devolução, etc.). É vital para analisar a "saúde" da operação e identificar gargalos.
* **Cerca (Geofencing):** Exibe a porcentagem de documentos comprovados *dentro do raio permitido* em relação ao endereço do destinatário. Se o motorista comprovar a nota muito longe do endereço, a cerca fica negativa (esse raio de tolerância será personalizável por distribuidor no futuro).
* **Romaneios:** Informa o status das viagens geradas: quantos estão na Expedição (ainda não saíram), Em Viagem (com motorista) e Concluídos.

**Interatividade:**
* Quase todos os gráficos e quadrantes são clicáveis. Ao clicar em um indicador (ex: "10 notas em auditoria"), um modal se abre listando os documentos específicos.
* O final da tela exibe uma lista detalhada baseada no agrupamento selecionado (ex: desempenho por motorista). O botão `+` abre detalhes de um registro específico.

### 1.2. Dashboard Analítico
Focado em uma visão mais ampla e gerencial da operação. Apresenta indicadores de performance, desde o **OTIF** (On Time In Full - no prazo e completo) até indicadores divididos por fase do ciclo do pedido.

---

## 2. Módulo: Usuários

Responsável pelo gerenciamento de funcionários e acessos à plataforma.
A tela inicial lista todos os usuários cadastrados e possui o botão "Criar Usuário".

### 2.1. Criação e Gestão
* **Login/Senha Inicial:** Ao criar um usuário, a senha padrão será igual ao login (CPF ou CNPJ). No primeiro acesso, o sistema exigirá a troca da senha.
* **Permissões:** O cadastro é dividido em informações pessoais (esquerda) e definições de base/módulos (direita). Por padrão, o sistema sugere módulos baseados no "Tipo" e "Área" do usuário, mas as permissões podem ser personalizadas (adicionando ou removendo módulos num modal).
* **Edição em Massa:** É possível selecionar múltiplos usuários via *checkbox* na tabela para alterar suas bases simultaneamente.

### 2.2. Tipos de Usuários
1. **Motorista:** O *único* tipo que possui acesso ao Aplicativo Motorista (Mobile) para receber romaneios e comprovar entregas.
2. **Colaborador:** Possui 4 áreas diferentes de atuação, cada uma com um conjunto padrão de módulos liberados.
3. **Gestor:** Similar ao Colaborador, porém com mais privilégios, incluindo a permissão para acessar o próprio módulo de cadastro de Usuários.
4. **Embarcador:** Acesso externo limitado que um cliente (transportador) concede ao seu embarcador (indústria/distribuidor). Permite que o embarcador veja apenas informações de *suas próprias notas*, geralmente restrito ao módulo de Pesquisa.

---

## 3. Módulo: Romaneio

O romaneio agrupa documentos e é o pilar da movimentação de carga no setor de transportes. Qualquer movimentação exige um romaneio. Ao criar um romaneio na Plataforma Web, ele é imediatamente sincronizado para o Aplicativo do Motorista associado.

### 3.1. Tipos de Romaneio na Azapfy
1. **Coleta:** O transportador busca a carga na distribuidora e leva para a base da transportadora.
2. **Transferência:** A distribuidora (com motorista próprio) leva a carga até a base do transportador.
3. **Transbordo:** A carga é movida de uma base da transportadora para *outra base da mesma transportadora*.
4. **Entrega:** A fase final. O motorista leva a carga ao destinatário final para colher a assinatura.
5. **Redespacho:** Uma transportadora leva a carga para *outra transportadora* (terceirizada) que fará a entrega.

### 3.2. Fluxo de Criação e Edição
* **Geração:** Clica-se em "Criar Romaneio". Após preencher dados básicos, insere-se os documentos "bipando" códigos de barras ou através do botão "Pesquisar Documentos" (buscando em lote).
* **Exportação:** Ao finalizar a criação, o sistema exibe dados estratégicos e permite exportar o romaneio em **PDF ou Excel** personalizável.
* **Edição:** Na lista de romaneios, é possível excluir ou editar (adicionar/remover notas) viagens já criadas.

### 3.3. Validação de Romaneios do App
Motoristas podem criar romaneios "avulsos" diretamente no aplicativo, sem intervenção prévia do backoffice. O sistema Web possui o botão **"Validar Romaneio"**, que lista essas viagens criadas no celular. Se um colaborador aprovar, o romaneio se torna definitivo. Se rejeitar, os documentos voltam ao status de pendentes.

---

## 4. Módulo: Pesquisa

O Módulo de Pesquisa é o coração do sistema, sendo a ferramenta mais utilizada pelos usuários de backoffice para varrer todo o banco de dados e aplicar ações corretivas.

### 4.1. Funcionalidades da Tabela
* **Filtros e Colunas:** Altamente customizável. O botão "Filtro" ajusta a busca e o "Mostrar Colunas" define o que aparece na tabela de resultados.
* **Ações em Massa:** Botões para "Enviar E-mail" (envia a foto da comprovação) e "Exportar Excel" (gera planilha com o resultado da tela).
* **Paginação Inteligente:** Devido ao volume de dados, a tabela carrega as notas de **500 em 500 registros** (botão "Carregar Mais"). Há um botão "Buscar Todos", mas seu uso pode gerar lentidão no processamento.

### 4.2. Visão Detalhada do Documento (Botão "+")
Ao clicar no detalhe de uma nota específica, um novo menu de ações se abre no lado direito:
* Download (PDF ou XML) do documento fiscal.
* Botão de Impressão.
* **Inclusão/Edição Manual:** Permite que o colaborador inclua uma comprovação manualmente (caso o motorista tenha tido problemas com o App). Se a nota já estiver comprovada, permite excluir a última comprovação registrada ou editá-la.

### 4.3. Botões Estratégicos (Rastreabilidade)
* **Histórico:** Abre um modal que é o dossiê da nota. Exibe o mapa de onde a entrega ocorreu, a foto do canhoto e o **Tracking completo** do ciclo de vida da carga (status, data, hora e local).
* **Prazo:** Exibe informações de SLA da entrega.
* **Ocorrências:** Mostra todas as tratativas, reclamações ou apontamentos especiais atrelados àquele documento pelo módulo de SAC.
