# gqlgen改造，支持多级子目录生成resolver文件，方便管理大型项目

### 下载
`git clone https://github.com/jemuelmiao/gqlgen.git`

`cd gqlgen`

`go build`

`进入待解析代码目录`

`[gqlgen.exe | gqlgen] generate`

### 配置
相比原版gqlgen，gqlgen.yml中需调整的配置项如下：
1. resolver模块增加配置项：schema_dir，用于将schema文件夹根目录对应到resolver文件夹根目录
2. resolver模块去掉resolver_template，内部使用三个template：resolver_interface.gotpl、resolver_schema.gotpl、resolver_single_file.gotpl
3. resolver模块去掉package，根据schema中各目录名确定resolver中各包名

### 示例
1. gqlgen.yml配置

![image](https://github.com/jemuelmiao/gqlgen/assets/28854032/33a3c398-5e5a-4df4-afaf-868bafca62c2)

![image](https://github.com/jemuelmiao/gqlgen/assets/28854032/d6efe785-96ec-4111-bfd0-e8083d0f367f)

2. 组织结构
<img width="306" alt="image" src="https://github.com/jemuelmiao/gqlgen/assets/28854032/ad9adcfc-f900-46b5-865d-a6ebb43a497b">
