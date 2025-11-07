## Geth存储方式更改

### 1.项目背景
本项目基于 go-ethereum（Geth）源码进行修改，实验性地引入 RaptorQ 编码机制，
将原始区块数据在写入数据库前进行编码并存入数据库。
目的是探索区块链数据在raptorq下能否高效存储。

### 2.修改位置与核心逻辑
1. `core\blockchain`

这是本次修改的核心位置，修改有以下几点。
- 修改了函数`writeBlockWithState`的核心逻辑，保持原本的储存逻辑不变。同时新添加`BlockBatchCache`作为储存原码块的cache。在存储的原码块满四个之后，可以直接通过`EncodeBlock`得到`encodedSymbols`。然后通过新写的`writeEncodedBlock`函数将编码后的符号按照原码的`Hash`和`NumberU64`存储到数据库当中。
- 在`blockchain`里添加了数据结构`BlockBatchCache`，用于存储`blockbatch`中的数据。其中实现的核心函数`EncodeBlock`，可以将存储的四个原码块通过raptorq编码产生对应的符号返回。具体实现来讲就是将原本的`BlockBatchData`转化为新的数据结构`blockBatchDataRLP`，通过RLP编码将原本的数据转化为`[]byte`，再将这些`[]byte`补0对齐之后扔进raptorq编码器里，得到对应的符号即可。
2. `core\rawdb`
为了存储编码块，在数据库里添加了新的kv对，具体修改如下。
- 在`rawdb/accessors_chain.go`里添加了新的函数`WriteEncodedBlock` `ReadEncodedBlock` `DeleteEncodedBlock`供`blockchain`调用。
- 修改了`rawdb/schema`，添加了新的k`blockEncodedBlockPrefix = []byte("eb") // eb = encoded block`，添加了函数`blockEncodedBlockKey`。

### 3.构建和运行
在根目录下运行以下命令：
```bash
make geth
cd data
geth --datadir node1 \
--port 30306 \
--bootnodes enode://efbf2f1ec96876790cd805502b2d8bb03a61f5fcc61b2692e6736d5a70202e8eda7eb16b8b448244363d92c31a5c322988d2ae628f9cf9329dc02bf27039850f@127.0.0.1:0?discport=30305 \
--networkid 123454321 \
--unlock 0x92424e4f86201c931ba53eb48619d93244c18e13 \
--password node1/password.txt \
--authrpc.port 8551 \
--mine \
--miner.etherbase 0x92424e4f86201c931ba53eb48619d93244c18e13 \
--miner.gasprice 0
```
启动一个矿工节点，可以观察到到一定数量的块之后就会产生编码块存储成功的info。
  