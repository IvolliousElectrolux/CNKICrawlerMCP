package types

type Paper struct {
	Title                     string   // 论文标题（纯文本，不含 HTML 标签）
	Authors                   []string // 作者列表
	Source                    string   // 来源（期刊/会议名称）
	SourceLink                string   // 来源链接
	Year                      int      // 发表年份，如 2023；0 表示知网未给出可解析的年份
	Date                      string   // 发表时间原文，如 "2023-05-15"、"2026-10-16 10:52"
	OnlineFirst               bool     // 网络首发：尚无正式刊期，Year 为网络首发年而非刊期年份
	SouceCompositeFactor      float64  // 复合影响因子
	SourceComprehensiveFactor float64  // 综合影响因子
	Href                      string   // 跳转链接
}
type ScanPostParam struct {
	QueryJson  string
	PageNum    int
	PageSize   int
	ProductStr string
	SearchFrom string
	Turnpage   string // 翻页触发人机验证关键参数 第9页的turnpage = vLP2bNpghntZLRq9Q5Y7Qg!! 可查1~17页
}
type QueryJson struct {
	Platform   string                 `json:"Platform"`
	Resource   string                 `json:"Resource"`
	Classid    string                 `json:"Classid"`
	Products   string                 `json:"Products"`
	QNode      QNode                  `json:"QNode"`
	ExScope    int                    `json:"ExScope"`
	SearchType int                    `json:"SearchType"`
	Rlang      string                 `json:"Rlang"`
	KuaKuCode  string                 `json:"KuaKuCode"`
	Expands    map[string]interface{} `json:"Expands"`
	SearchFrom int                    `json:"SearchFrom"`
}
type QNode struct {
	QGroup []QGroup `json:"QGroup"`
}
type QGroup struct {
	Key        string `json:"Key"`
	Title      string `json:"Title"`
	Logic      int    `json:"Logic"`
	Items      []Item `json:"Items"`
	ChildItems []Item `json:"ChildItems"`
}
type Item struct {
	Field    string `json:"Field"`
	Value    string `json:"Value"` // 搜索的关键字
	Operator string `json:"Operator"`
	Logic    int    `json:"Logic"`
	Title    string `json:"Title"`
}
type LoginResponse struct {
	IsSuccess    bool           `json:"IsSuccess"`
	Msg          string         `json:"Msg"`
	ErrorCode    int            `json:"ErrorCode"`
	ErrorMsg     string         `json:"ErrorMsg"`
	Uid          string         `json:"Uid"`
	UserName     string         `json:"UserName"`
	ShowName     string         `json:"ShowName"`
	UserType     string         `json:"UserType"`
	BUserName    string         `json:"BUserName"`
	BShowName    string         `json:"BShowName"`
	BUserType    string         `json:"BUserType"`
	IsAutoLogin  bool           `json:"IsAutoLogin"`
	IsShowCheck  bool           `json:"IsShowCheck"`
	R            string         `json:"r"`
	Domain       string         `json:"Domain"`
	Expire       string         `json:"Expire"`
	Vcode        string         `json:"Vcode"`
	IsSubUser    *bool          `json:"IsSubUser"`
	Members      *[]interface{} `json:"Members"`
	TurntoUrl    string         `json:"TurntoUrl"`
	TurntoTarget string         `json:"TurntoTarget"`
}
type PaperInfo struct {
	CAJ      string // CAJ 下载链接
	PDF      string // PDF 下载链接
	HTML     string // HTML 在线阅读链接
	Abstract string // 摘要
	Year     int    // 发表年份，取自详情页题录行；0 表示未解析到
	// 网络首发：该论文尚无正式刊期，详情页没有题录行，Year 取自"网络首发时间"
	OnlineFirst bool
}
type PaperMainBodyParams struct {
	Invoice   string `json:"invoice"`
	FileName  string `json:"fileName"`
	TableName string `json:"tableName"`
	DbCode    string `json:"dbCode"`
}
type SourceInfo struct {
	Name string // 来源名称
	Link string // 来源链接（绝对路径）
}

/*
# QueryJson示例结构
{
    "Platform": "",
    "Resource": "CROSSDB",
    "Classid": "WD0FTY92",
    "Products": "CJFQ,CAPJ,WWJD,CJTL,CDFD,CMFD,CPFD,IPFD,CPVD,WWPD,CCND,SCSF,SCHF,SCSD,SOSD,SNAD,CCJD,WBFD,WWBD,CCVD,CJFN",
    "QNode": {
        "QGroup": [
            {
                "Key": "Subject",
                "Title": "",
                "Logic": 0,
                "Items": [
                    {
                        "Field": "SU",
                        "Value": "格罗皮乌斯",
                        "Operator": "TOPRANK",
                        "Logic": 0,
                        "Title": "主题"
                    }
                ],
                "ChildItems": [

                ]
            }
        ]
    },
    "ExScope": 1,
    "SearchType": 2,
    "Rlang": "BOTH",
    "KuaKuCode": "YSTT4HG0,LSTPFY1C,JUP3MUPD,MPMFIG1A,WQ0UVIAA,BLZOG7CK,PWFIRAGL,EMRPGLPA,NLBO1Z6R,NN3FJMUV",
    "Expands": {

    },
    "SearchFrom": 4
}
*/
