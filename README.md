# cri_extract

从 CRIWARE `ACB/AWB` 容器中提取音频:还原 **Cue 原始文件名&提取元数据**,并通过
[vgmstream](https://github.com/vgmstream/vgmstream) 解码成 wav。   
！！！因为相关资源缺乏创作者信息（搓这个只是为了提取一首没有公开的剧情界面配乐&剧情曲），故没有实现元数据写入功能，若有需求请自行借助本项目导出的 manifest.json 实现！！！

> 为什么要用 vgmstream 解码:在提取某游戏的音频时发现其是较新 CRI 编码器产出的 HCA（header 带 0x80 掩码标记、部分含 ciph 标记但音频帧本身未加密），尝试使用 FFmpeg （9.0.1,
> 含 CRI HCA 解码器）发现没法正常解码,而 vgmstream 无需任何密钥（因为数据帧本来就没有真正加密）即可正确解码，懒得排查问题，故将解码工作丢给vgmstream

## 特性

- 解析 ACB 的 `@UTF` 表(含嵌套表递归展开),读取 `CueNameTable`、
  `CueTable`、`WaveformTable` 等,按 vgmstream 参考语义还原
  **cue → AWB 段** 映射(支持 ReferenceType 1/2/3/8)。
- 定位音频容器 AWB,优先级:
  1. ACB 内嵌 `AwbFile`;
  2. 同目录同名 `.awb`;
  3. `StreamAwbHash`(AWB 内容 MD5)在目录内自动配对 —— 适用于
     "文件名是内容 SHA-1" 的散装音频资源（比如kuro的某五字游戏）。
- 输出命名:
  - 单 cue: `<cue名>.hca` / `.wav`;
  - 多段 cue: `<cue>_#1…`;
  - 同一段被多个 cue 引用: `cueA&cueB`;
  - 无法命名的段: `<ACB名>__seg<N>`。
- 元数据清单 `manifest.json`:ACB/AWB 哈希、cue 名、每段的 EncodeType /
  Streaming / 声道 / 采样率 / NumSamples / LoopFlag / 偏移 / 大小等。
- **多音轨 ACB 默认全部导出**,可用 `-first-track` 只导出第一条。

## 用法

```bash
go build -o cri_extract.exe ./cmd/cri_extract

cri_extract -in <素材目录> -out <输出目录> -vgmstream <vgmstream-cli路径>
```

### 参数

| 参数 | 默认 | 说明 |
|---|---|---|
| `-in` | (必填) | 含 `.acb`/`.awb` 的目录 |
| `-out` | `<in>_out` | 输出目录(写入 wav/hca 与 `manifest.json`) |
| `-vgmstream` | 自动查找 | `vgmstream-cli[.exe]` 路径;找不到时可用 `-format meta` 免解码 |
| `-format` | `wav` | `wav`(默认) / `hca` / `both` / `meta`(只解析并导出清单) |
| `-first-track` | `false` | 多音轨 ACB 只导出第一条 |
| `-threads` | CPU 数 | 并行解码 vgmstream 的进程数 |
| `-limit` | 0 | 只处理前 N 个 ACB(调试用) |
| `-loops` | `false` | 默认按完整单遍解码(`-i`);开启后保留循环信息交给 vgmstream |

### 示例

```bash
# matrix 全量:全部音轨 -> wav(完整单遍)
cri_extract -in D:\work\PyCriCodecs\matrix -out D:\work\PyCriCodecs\matrix_out_wav

# 只导出每条 ACB 的第一条音轨
cri_extract -in D:\work\PyCriCodecs\matrix -out ... -first-track

# 只解析并生成命名/元数据清单,不解码
cri_extract -in ... -out ... -format meta

# 保留 hca 与 wav
cri_extract -in ... -out ... -format both
```

## 布局

```
cri_extract/
├── go.mod                       # Go module (cri_extract)
├── README.md
├── cri_extract.exe              # 构建产物(go build 后生成)
├── cmd/
│   └── cri_extract/
│       └── main.go              # CLI 入口:参数解析、vgmstream 自动查找
└── internal/
    ├── criutf/
    │   ├── utf.go               # @UTF/@EUTF 表解析(含嵌套/掩码类型处理)
    │   └── utf_test.go          # 用真实 ACB 样本的解析单测
    ├── criacb/
    │   └── acb.go               # ACB 容器:嵌套表展开、cue→AWB 段映射
    ├── criawb/
    │   └── awb.go               # AFS2(AWB)容器:ids/offset 表、分段
    └── work/
        ├── work.go              # 配对、命名、提取、manifest
        └── decode.go            # vgmstream-cli 并发解码(断点续跑)
```

