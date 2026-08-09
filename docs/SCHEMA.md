# 스키마와 생성 계약

payday가 무엇을 소유하고, 선언 하나에서 무엇이 나오는가.
이 문서가 담는 절: §2~§4. 돌아가기: [DESIGN.md](../DESIGN.md)

> 절 번호는 문서를 나누기 전의 것을 그대로 쓴다 — 전역으로 유일하므로 `§7`은
> 어느 문서에 있든 같은 절을 가리킨다.

## 2. payday가 소유하는 스키마

`Tenant`(벽)와 `Holder`(행위자)와 `Audit`(흔적)은 payday가 정의한다. 앱이 다시 선언하지
않는다.

```proto
// payday/proto/payday/holder.proto  —  베이스. 앱이 여기서 생성한다.
edition = "2023";
package payday;

message Holder {
  bytes  id     = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  Tenant tenant = 2 [(orm.edge) = {immutable: true}];
  string alias  = 4;
  ...
  // 8..12, 16.. 은 앱의 자리. 나머지는 payday의 것이고 override되면 안 된다.
  reserved 3;

  option (orm.message)      = {rpc: {crud: true}, indexes: [...]};
  option (payday.entity) = {domain: 2};
}
```

### 무엇을 소유하는가 — 계약이지 Go 타입이 아니다

앞선 판에서는 payday가 `*paydaypb.Holder`라는 **Go 타입까지** 소유하는 것으로 그렸다.
그러면 `frame`·`auth`·`gate`가 구체 타입을 쓸 수 있어 제네릭이 사라지는 대신, 앱이 필드를
더할 수 없다. 그것을 "받아들이지 않으려면 통째로 포기해야 한다"고 썼는데, **틀렸다.**
`protobuf-merge`는 메시지에 필드를 붙이는 것을 이미 지원한다 — 같은 이름의 메시지를
병합하고, 새 필드는 뒤에 덧붙이고, 번호가 겹치면 오버레이가 이긴다. go-app이 서비스에
쓰고 있는 바로 그 기계장치다.

```proto
// app/proto/ext/payday/holder.ext.proto
message Holder {
  string email = 8;
  bytes  idp_subject = 9;
}
```

그래서 결론이 바뀐다. **가져오되, payday가 소유하는 것은 proto 계약이고 Go 타입은
앱의 것이다.**

| payday의 것 | 앱의 것 |
| --- | --- |
| `holder.proto` 베이스 — 필드 1·2, 4..7, 13..15, 인덱스, 도메인 번호 | `holder.ext.proto` — **필드 3**, 8..12, 16.. |
| 의미 — 테넌트는 벽이다, Holder는 softly erase되고 그 이유, 감사는 행위자의 것이다 | 그 위의 도메인 규칙 |
| 생성되는 벽 술어·감사 레코더·auth 리졸버 | 정책 |
| 생성된 Go 타입은 **앱 패키지에** 떨어진다 (`go_app.Holder`) | |

이렇게 하면 payday 런타임은 `*Holder`를 이름으로 부를 수 없다. 대신 **원시값으로 말한다**:

```go
type Frame struct {
    ActorId  pdid.Id          // domain == Holder
    TenantId pdid.Id          // domain == Tenant
    Actor    proto.Message  // 정책이 자기 타입으로 assert 한다
    Grant    Grant
    Scope    Tenants
}
```

**제네릭은 여전히 없다.** `proto.Message`는 생성된 모든 메시지가 만족하는 실재하는
인터페이스이고, 프레임이 실제로 쓰는 것은 두 개의 ID뿐이다 — 벽이 좁히는 것도,
레코더가 찍는 것도, 요율 제한이 세는 것도 전부 ID다. 구체 타입이 필요한 유일한 자리는
앱 자신의 `Policy`이고, 거기서는 앱이 양쪽을 다 갖고 있으므로 지역적인 타입 단언 한 줄이다.

접근자 인터페이스(`GetTenant() TenantLike` 같은)로 풀지 않는 이유는 Go가 **반환 타입에
공변이 아니기** 때문이다. `func (h *Holder) GetTenant() *Tenant`는 그런 인터페이스를
만족하지 않는다. 메시지 그래프를 인터페이스로 뚫으려면 어댑터가 필요하고, 원시값을
들고 있으면 그 문제가 아예 없다.

### 2.1 3번 필드는 앱의 것이다 — 테넌트보다 작은 집합

헤더는 **타입을 모르는 행에서도 읽을 수 있는 것**이다. 1은 키, 2는 테넌트, 4는 별칭,
5·6은 이름과 설명, 7은 라벨. `header.Of`가 어떤 엔티티에서든 그것들을 읽는다.

**3은 비어 있고, payday의 것이 아니다.** 앱이 **테넌트보다 작은 집합**을 둘 자리이고,
payday는 여기에 아무것도 쓰지 않는다.

왜 8..12가 아니라 3인가. 8..12도 앱의 것이지만 그것은 **그 앱의** 번호이고 공유된 뜻이
없다. 3에 두면 타입을 모르는 읽는 쪽이 "이 행이 속한 집합"을 일반적으로 물을 수 있는
유일한 번호가 된다. 지금 `header.Of`가 그것을 읽지 않더라도, 읽게 만들 수 있는 자리가
하나는 있어야 한다.

#### 넣는 법

payday의 엔티티에는 오버레이로, 자기 엔티티에는 직접.

```proto
// proto/ext/payday/holder.ext.proto
message Holder {
  Site site = 3 [(orm.edge) = {}];
}
```

```proto
// proto/app/asset.proto
message Asset {
  bytes  id     = 1 [(orm.field) = {type: TYPE_UUID, key: true, default: ""}];
  payday.Tenant tenant = 2 [(orm.edge) = {immutable: true}];
  Site   site   = 3 [(orm.edge) = {}];
  string alias  = 4;
  // ...
}
```

`Site` 자체는 앱의 평범한 엔티티다. `pd entity add --tenanted Site .`

#### 좁히는 법 — 벽을 하나 더 얹는다

**payday에 두 번째 격리 축을 넣을 필요가 없다.** 생성된 `Scope`가 엔티티마다 술어를
내는 인터페이스이고, `Scopes`가 그것들을 AND로 합성한다.

```go
bare.WithScope(bare.Scopes{pd.Wall(), SiteWall{}})
```

```go
// SiteWall은 앱의 것이다. payday는 이 축을 모른다.
type SiteWall struct{ bare.Unscoped }

func (SiteWall) AssetScope(ctx context.Context) (predicate.Asset, error) {
    f, ok := frame.From(ctx)
    if !ok {
        return nil, nil
    }

    // 리졸버가 읽어온 행. `Frame.Row`가 있는 이유가 이것이다 --
    // 손에 쥔 식별자가 아니라 데이터베이스에서 읽어온 것으로 판단한다.
    h, ok := f.Row.(*app.Holder)
    if !ok {
        return nil, nil
    }

    return asset.SiteIDEQ(h.GetSite().GetId()), nil
}
```

셋이 중요하다.

- **`bare.Unscoped`를 임베드한다.** 할 말이 있는 엔티티만 쓰면 되고, 나중에 스키마에
  추가되는 엔티티가 이 파일을 깨뜨리지 않는다. 그게 없으면 무언가를 좁히는 모든 앱이
  엔티티가 늘 때마다 컴파일이 깨지고, 고치는 방법은 "의견 없음"이라고 적는 메서드다.
- **쓰기는 술어가 아니라 레이어에서 막힌다.** `Scope`는 술어이므로 `Get`·`List`·
  `Patch`·`Erase`를 좁힌다. `Add`에는 WHERE가 없어서 생성된 `Gate`가 대신 막는다 —
  이 엣지가 가리키는 집합을 벽 너머로 읽고, 못 보면 `NotFound`. 자세한 것은 §2.2.
- **`WithScope`는 두 번 못 준다.** 두 번 주면 `ErrTwice`이고 에러가 `Scopes{...}`를
  가리킨다 — 둘 중 하나를 조용히 잃는 것이 이 자리에서 가장 나쁜 답이기 때문이다.

#### 2.1.1 트레일은 두 당사자의 것이다

`tenanted:`의 `field`는 여러 개일 수 있고, 여럿이면 **OR**이다 — 그 행은 자기가 이름 부르는
**아무** 테넌트의 벽 뒤에 있다.

이것이 payday에서 행을 **더** 보이게 만드는 유일한 구조라서, 무엇을 위한 것이 아닌지부터
적어둔다. **두 테넌트가 한 행을 공유하는 것은 payday가 모델링하지 않는다** — 주인이 없고,
누가 지울 수 있는지에 답이 없고, "행은 테넌트에 속한다"는 문장이 성립을 멈춘다.

이것이 있는 이유는 하나다. **한 번의 쓰기에는 행이 바뀐 테넌트와 그것을 바꾼 행위자의
테넌트가 있고, 둘 다 그 기록의 당사자다.** 본사 운영자가 고객의 행을 고치면 고객은 자기에게
무슨 일이 있었는지 읽을 수 있어야 하고, 본사는 자기 사람이 무엇을 했는지 읽을 수 있어야
한다. 어느 쪽도 그러기 위해 **상대를 볼 수 있을 만큼 넓은 범위를 들 필요가 없어야** 한다.

```proto
tenanted: {field: "tenant_id", field: "actor_tenant_id"}
```

생성되는 술어는 `audit.Or(audit.TenantIDIn(vs...), audit.ActorTenantIDIn(vs...))`이다.

읽기는 둘 중 한쪽으로 필터하므로 인덱스가 먹는다 — `WHERE (a IN vs OR b IN vs) AND a = me`는
`(a, date_created)` 인덱스를 쓰고 OR은 잔여 필터가 된다. **그래서 컬럼마다 인덱스가 하나씩
있어야 한다.** 하나만 두면 읽기의 절반이 테이블을 훑는다.

#### 2.2 벽은 술어이고, `Add`에는 술어가 없다

그래서 `Add`는 **레이어**에서 막힌다. 나머지는 전부 쿼리라 술어가 붙는다.

| | 어디서 |
| --- | --- |
| `Get`·`List`·`Watch` | 술어 — WHERE가 붙는다 |
| `Patch`·`Apply`·`Erase` | 술어 — 대상을 고르는 것이 WHERE다 |
| **`Add`** | **`Gate` 레이어** — INSERT에는 WHERE가 없다 |

`Gate`가 생성하는 것은 벽 뒤에 있는 모든 엔티티의 `Add` 앞에서 **테넌트에 닿는 경로의
첫 홉을 벽 너머로 읽는 것**이다. 보통은 그 행의 `tenant` 엣지이고, 다른 행을 거쳐 테넌트에
닿는 엔티티라면 그 행이다. 못 보면 `NotFound`다 — 거절이 아니라, 그 행이 존재한다는 것
자체가 볼 수 없는 caller에게 알려줄 일이 아니기 때문이다.

집합(3번 필드)을 선언했다면 그것도 같이 읽는다. payday가 3번을 격리 축으로 읽기로 했으므로
한 단계 아래의 같은 규칙이다. **평범한 엣지는 검사하지 않는다** — 내 행이 남의 행을
가리키는 것은 참조 무결성의 문제이지 테넌시의 문제가 아니고, 물어보려면 쓰기마다 엣지당
읽기가 하나씩 붙는다.

비용은 `Add`당 읽기 하나다. 서버가 시간을 쓰는 경로가 아니고, 대안은 읽기에는 성립하고
쓰기에는 성립하지 않는 규칙이다.

> **이것은 오래 열려 있었다.** 검사는 payday 자신의 `Holder`에만 있었고 앱의 엔티티로
> 일반화되지 않았다. 그래서 caller가 볼 수 없는 테넌트에 행을 심을 수 있었다 — 읽기
> 유출은 아니지만(모든 읽기가 좁혀지므로 심은 사람에게도 즉시 안 보인다, 그래서 아무도
> 눈치채지 못했다) 피해자가 자기 것으로 읽는 데이터, 피해자가 무는 사용량, 그리고
> 감사 행이 **행위자의 테넌트**에 찍히므로 피해자의 트레일에는 아무것도 안 남는 것이
> 남는다. 회귀 테스트는 `internal/apptest/cmd/layers_test.go`에 있다.

#### 왜 payday가 흡수하지 않는가

두 번째 격리 축은 기능 추가가 아니라 재작성이다. `frame.Narrow`의 단일 리스트 계약,
`Grant`의 두 축, `gate.ByTenant`의 단일 키, `audit.Row`의 단일 테넌트, `slug`의 2단
`@tenant/alias`, `alias`의 테넌트별 유일성, `checkVia`의 단일 대상, 그리고 생성되는 모든
`<E>Scope`가 전부 하나를 전제한다.

그리고 대개 그것이 필요한 앱에게도 답이 아니다. Site가 **배포 단위**라면 [TENANCY.md]의
답이 더 강하다 — 배포를 나누고, 격리를 술어가 아니라 **행의 부재**로 만드는 것. 술어는
빠뜨릴 수 있고 없는 데이터는 빠뜨릴 수 없다. 두 축이 실제로 일하는 곳은 여러 Site를 한
번에 보는 관제 평면 하나뿐이고, 거기서는 이 절의 `Scopes`로 충분하다.

### 하나 남는 경계: ent

메시지가 앱의 것이 되면서 이 경계는 오히려 자연스러워졌다. ent는 앱마다 하나의 스키마
집합에서 하나의 `*ent.Client`를 만들고 `predicate.Holder`도 거기서 나온다. 그러니
`*ent.Client`나 `predicate.*`를 이름으로 불러야 하는 것 — **벽 술어와 감사 레코더** —
은 payday 모듈에 있을 수 없고 앱에 **생성**된다. payday 플러그인이 찍고, 찍힌 코드가
payday 런타임의 함수를 부른다. `Recorder`와 `Scope`가 이미 그 모양이므로 새 규율은 아니다.

Holder의 ent 스키마도 앱에서 생성된다 — 병합된 proto에서 나오므로 `email` 컬럼이 함께
생긴다. payday가 별도의 스키마 패키지를 낼 필요가 없다.

### 레이어도 payday의 것이다 — 단, 생성된다

`gate`(누가 무엇을 볼 수 있나)와 `audit`(무엇이 바뀌었나)은 Tenant/Holder/Audit이
payday의 것인 이상 payday의 것이다. go-app의 `server/gate`가 실제로 담고 있는 규칙을
보면 전부 Tenant와 Holder에 대한 문장이다 — 테넌트는 안에서 세우거나 내리는 것이
아니다(`Unimplemented`), Holder는 자기 테넌트 안에만 더할 수 있다. 앱의 규칙이 아니다.

그런데 **레이어 타입 자체는 payday 모듈에 있을 수 없다.** 레이어는
`struct { go_app.Overlay }`이고 `go_app.Server`는 앱에 생성되는 인터페이스다. payday가
그 이름을 부를 수 없고, 타입 파라미터로도 풀리지 않는다 — 오버레이는 앱의 서비스까지
전부 전달해야 하는데 payday는 그것이 무엇인지 모른다.

그래서 §2의 다른 것들과 같은 답이다. **껍데기는 생성되고, 판단은 런타임에 있다.**

| | payday 런타임 (읽히는 곳) | 앱에 생성 (얇은 곳) |
| --- | --- | --- |
| `gate` | `Policy`, `Call`, `Grant`, `Tenants`, `Interceptor`, `ByTenant`, 규칙 함수들 | `Server` 오버레이 + `WithDriver` + `Build()` + Tenant/Holder RPC 오버라이드 + 엔티티별 벽 술어 |
| `audit` | `Recorder` 로직(무슨 필드를 채우나, trace id, patch 직렬화), `List`의 페이징·필터, 거절할 RPC 목록 | `Server` 오버레이 + `AuditAddRequest` 조립 + ent 쿼리 |

생성된 것이 얇을수록 좋다 — 판단이 런타임 소스에 있어야 읽을 수 있기 때문이다(§14).

**앱이 자기 규칙을 여기에 끼워 넣지 않는다.** 생성된 레이어는 편집 대상이 아니고,
앱의 규칙은 자기 `core` 레이어나 스택 앞에 세운 자기 레이어, 또는 주입하는
`gate.Policy`로 간다. 생성된 코드를 손대지 않게 두는 것이 재생성을 안전하게 만든다.

### 벽은 선언에서 생성된다

이것이 gate를 가져오면서 딸려 오는 가장 큰 소득이다.

go-app의 `wall.go`는 엔티티마다 `<E>Scope` 메서드를 손으로 쓰고, 그 주석이 스스로
문제를 적어 두었다 — 기본 구현을 임베드해 두었으므로 엔티티를 더하면 **말없이 벽 밖으로
나가고**, 그것을 테스트로 때우고 있다. 손으로 쓰는 한 이 둘 중 하나다: 빠뜨리면 조용히
새거나, 안 빠뜨리려고 컴파일을 깨거나.

세 번째 답은 **proto에서 선언하고 생성하는 것**이다.

```proto
message Robot {
  Tenant tenant = 2 [(orm.edge) = {immutable: true}];
  ...
  option (payday.entity) = {
    domain: 7
    tenant: {via: "tenant"}    // 이 엔티티는 테넌트 안에 있고, 엣지 이름은 tenant
  };
}
```

플러그인이 찍는 것:

```go
func (w wall) RobotScope(ctx context.Context) (predicate.Robot, error) {
    vs, all, err := w.ids(ctx)
    if all || err != nil {
        return nil, err
    }
    return robot.HasTenantWith(tenant.IDIn(vs...)), nil
}
```

테넌트 밖에 있는 엔티티는 그렇다고 말해야 한다 — `tenant: {none: {}}`. **둘 중 하나를
말하지 않으면 생성 실패**(§7). 벽에 대한 결정이 빠뜨릴 수 없는 것이 되고, 빠뜨렸을 때
새는 쪽이 아니라 멈추는 쪽으로 실패한다.

### 비용, 다시

"모양을 바꿀 수 없다"는 사라졌다. 대신 남는 것은 더 좁고 더 다루기 쉽다.

- **번호 범위를 지켜야 한다.** 병합은 번호로 맞추므로 앱이 `4`를 쓰면 payday의 `alias`를
  **조용히 덮어쓴다.** 이것은 §7의 강제 항목이다 — `pd gen`이 payday 소유 번호를
  건드리는 오버레이를 **거절**한다. 추가만 허용하고 override는 허용하지 않는다.
- **필드를 더할 수는 있어도 뺄 수는 없다.** `alias`, `tenant`, `date_erased`는 auth와
  벽이 읽으므로 사라지면 안 된다. 위와 같은 검사로 막힌다.
- **payday가 베이스에 필드를 더하면 앱의 번호와 부딪힐 수 있다.** payday는 자기 범위
  (1..7, 13..15) 밖으로 나가지 않는다고 약속하는 것으로 갚는다. 범위가 모자라면 그때는
  minor 버전을 올리며 앱이 옮기는 수밖에 없다.

  범위가 원래 `14..15`였는데 하나 넓혔다. `version: {}` 필드가 들어가야 하기 때문이다 —
  §10.5에서 로컬 스토어가 늦게 도착한 옛 상태로 새 상태를 덮지 않으려면 필요하고,
  §7의 4h가 `watch:`를 선언한 엔티티에 그것을 요구한다. 지금 넣는 것은 공짜이고
  나중에 넣는 것은 모든 앱의 마이그레이션이다. `orm`의 `version`은 서버가 찍는
  타임스탬프이고 문서가 그것을 assign하는 것을 거부하므로, 쓰기당 스탬프 하나 말고는
  값이 붙지 않는다.

### 전제 확인 — 패키지를 넘는 엣지: **된다** (검증 완료)

§2 전체가 걸려 있던 단 하나의 기술적 전제였다. 앱의 엔티티가 다른 proto 패키지의
`payday.Tenant`를 엣지로 참조할 수 있어야 한다.

```proto
import "payday/tenant.proto";
message Robot {
  payday.Tenant tenant = 2 [(orm.edge) = {immutable: true}];
}
```

두 패키지(`base`/`app`)로 최소 예제를 만들어 `buf generate` → `ent generate` →
`go build`까지 통과시켰다. **생성기의 한계가 아니었고, 설정 조건이 둘 있다.**

#### 조건 1 — `strategy: all`

buf의 기본값은 `strategy: directory`이고, 그러면 **플러그인이 디렉터리마다 한 번씩**
호출된다. 계측해 보면 이렇다.

```
[1회차] app/robot.proto  generate=true    base/tenant.proto generate=false
[2회차] base/tenant.proto generate=true   (robot은 대상 아님)
panic: generated filepath to the entity not found: base.Tenant
```

`protoc-gen-orm-service`는 생성 대상 파일만 경로 표에 등록하는데, 1회차에서
`base/tenant.proto`가 대상이 아니라 등록되지 않는다. 엣지의 대상이 **다른 디렉터리에
있을 때만** 나타나므로 go-app은 한 번도 만나지 않았다 — 모든 proto가 한 디렉터리에 있다.

```yaml
plugins:
  - local: [...]
    out: .gen
    strategy: all      # ← 이것
```

**`pd gen`이 buf를 대신 부르므로 앱은 이것을 몰라도 된다**(§6). 다만 상류에 낼 것이
하나 있다 — 저 패닉 메시지는 원인을 말하지 않는다. "참조된 엔티티의 파일이 생성 대상이
아니다"라고 말했으면 이 조사가 필요 없었다.

#### 조건 2 — 한 앱의 모든 엔티티는 `go_package`를 공유한다

이것이 더 중요하다. 두 proto 패키지가 서로 다른 `go_package`를 가지면 생성은 "성공"하고
결과가 갈라진다.

```
.gen/basepb/internal/ent/schema/tenant.go     ← 스키마 패키지 둘
.gen/internal/ent/schema/robot.go
.gen/basepb/server/bare/store.g.go            ← 스토어 둘
```

그리고 `robot.go`는 여전히 `edge.To("tenant", Tenant.Type)`를 쓰는데 그 패키지에
`Tenant`가 없다. **컴파일되지 않는다.** ent는 앱마다 하나의 스키마 집합에서 하나의
클라이언트를 만들기 때문이고, §2가 이미 말한 경계가 여기서 물리적으로 나타난다.

그러니 **payday의 베이스 proto는 앱의 `go_package`로 생성된다.** `pd gen`이 정하고,
어긋나면 거절한다(§7). §2에서 "Go 타입은 앱의 것"이라고 한 결정과 정확히 같은 말이며,
그 결정이 선택이 아니라 **제약**이었다는 것을 확인한 셈이다.

## 3. 참조 — ID와 slug

한 행을 가리키는 방법 두 가지. 기계가 읽는 것은 128비트이고 사람이 읽는 것은
`@acme/admin#holder`다. **둘 다 도메인을 싣고, 둘 다 그것을 검증하고, 둘 다 같은 proto
선언에서 나온다** — 그것이 이 둘을 한 절에 두는 이유다.

| | 기계가 읽는 것 | 사람이 읽는 것 |
| --- | --- | --- |
| 형태 | `pdid.Id` — UUIDv8, 도메인은 바이트 9 | `@tenant/alias#domain` |
| 불일치하면 | `InvalidArgument` (§3.4) | `InvalidArgument` (§3.6) |
| 도메인의 출처 | `option (payday.entity)` | 같음 |
| 사는 곳 | 와이어, DB | 헤더, 설정 파일, 인증서, CLI, 로그 |

### 3.1 ID 구조

UUIDv8이다. v8은 표준이 "version/variant를 제외한 나머지 비트는 구현이 정한다"라고
정의한 자리이므로, 아래는 v7을 변형한 것이 아니라 **payday가 정의하는 레이아웃**이다.

```
 0                   1                   2                   3
 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1 2 3 4 5 6 7 8 9 0 1
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                          unix_ts_ms                           |  byte 0..3
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|          unix_ts_ms           |1 0 0 0|          seq          |  byte 4..7
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|1 0|    rand   |     domain    |             rand              |  byte 8..11
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
|                             rand                              |  byte 12..15
+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+-+
```

| 자리 | 비트 | 무엇 |
| --- | --- | --- |
| 0..5 | 48 | `unix_ts_ms` — 밀리초 단위 유닉스 시각 |
| 6 상위 | 4 | version = `8` |
| 6 하위 + 7 | 12 | `seq` — 같은 밀리초 안의 단조 증가 카운터 |
| 8 상위 | 2 | variant = `10` |
| 8 하위 | 6 | 난수 |
| **9** | **8** | **domain** |
| 10..15 | 48 | 난수 |

난수 54비트 + 밀리초당 4096개까지 순서가 보장되는 카운터.

**도메인이 바이트 9인 것은 `hid`를 따른다.** 문자열 표기에서 네 번째 그룹의 **마지막 두
자리**로 떨어져서 눈으로 읽힌다:

```
0199c3f4-2a10-8abc-8a03-9f2e1c4d5b6a
                    ^^
                    domain = 0x03
```

네 번째 그룹은 variant 때문에 늘 `8`/`9`/`a`/`b`로 시작하므로, 실제로 읽어야 하는 것은
뒤 두 자리뿐이다.

구현은 `google/uuid`의 v7 생성을 재료로 쓰되 두 바이트만 덮는다:

```go
func New(d Domain) Id {
    v := uuid.Must(uuid.NewV7())
    v[6] = 0x80 | (v[6] & 0x0F)   // version만 8로. 하위 4비트는 seq다.
    v[9] = byte(d)
    return Id(v)
}
```

**`hid`와 다른 유일한 지점이 저 `& 0x0F`다.** `hid.New`는 `v[6] = 0b1000_0000`으로 통째
덮는데, `google/uuid`의 `NewV7`은 같은 밀리초 안의 순서를 위해 바이트 6 하위 4비트와
바이트 7에 걸쳐 12비트 카운터를 넣는다. 통째로 덮으면 그중 상위 4비트가 날아가 밀리초당
256개까지만 순서가 유지된다. 표준을 어기는 것은 아니다 — v8은 그 비트들이 무엇이든
되게 두었으니 `hid`는 "seq가 8비트인 v8"을 정의한 것이고 그것대로 유효하다. 다만 재료가
공짜로 주는 12비트를 8비트로 줄일 이유는 없으므로 payday는 12비트를 쓴다.

`hid`에서 실제로 고쳐야 하는 것은 다른 쪽이다. **`Parse`/`From`이 아무것도 검증하지
않는다.** 16바이트면 무엇이든 `Id`가 되고, v4 UUID를 넣으면 바이트 9가 우연히 `3`이라서
"Holder"라고 주장한다. 도메인을 믿으려면 파싱이 version과 variant를 확인해야 한다.

```go
func Parse(s string) (Id, error)      // version==8 && variant==RFC4122 확인
func Of(v uuid.UUID) (Domain, bool)   // v8이 아니면 false. 도메인을 "모름"으로 만들지 않는다.
```

### 3.2 도메인은 proto 옵션에서 온다

`hid`의 `domain.go`는 상수와 `String()`과 `DomainString()` 세 곳을 손으로 맞춘다. 엔티티를
더할 때 하나를 빠뜨려도 컴파일된다. payday는 **선언 한 곳**에서 전부 만든다.

```proto
// payday/proto/payday/options.proto
edition = "2023";
package payday;
import "google/protobuf/descriptor.proto";
option go_package = "github.com/lesomnus/payday/paydaypb";

extend google.protobuf.MessageOptions {
  // orm은 45001/45101/45102를 쓴다. 겹치지 않는 블록을 잡는다.
  Entity entity = 46001;
}

message Entity {
  // 이 엔티티의 도메인. 1..255. 필드 번호와 같은 성격 — 한 번 정하면 바꾸지
  // 않고, 지운 엔티티의 번호는 재사용하지 않는다. 0은 "모름"이라 쓰지 않는다.
  uint32 domain = 1;

  // 이 엔티티가 테넌트 벽 안에 있는가. 반드시 둘 중 하나를 말해야 한다 — 말하지
  // 않으면 생성이 실패한다. 벽에서 빠지는 것은 결정이지 기본값이 아니다.
  oneof tenancy {
    Tenanted tenant = 2;   // {via: "<엣지 이름>"}
    Global   global = 3;   // 벽 밖. 그렇다고 말한 것.
  }

  // slug의 `#` 뒤에 오는 이름 (§3.6). 비면 메시지 이름을 kebab-case로.
  // 도메인 번호와 짝이므로 같은 곳에서 선언된다 — 하나의 선언, 두 개의 표기.
  string name = 4;
}
```

```proto
message Robot {
  ...
  option (orm.message)   = {rpc: {crud: true}};
  option (payday.entity) = {domain: 7};
}
```

payday 플러그인이 여기서 찍는 것:

```go
// app/go_app/domain.g.go
const (
    TenantDomain pdid.Domain = 1
    HolderDomain pdid.Domain = 2
    RobotDomain  pdid.Domain = 7
)

func init() {
    pdid.Register("go_app.Robot", RobotDomain)
    ...
}
```

그리고 생성 시점에 검사한다 — 이것이 §8의 강제 항목이 되는 자리다:

- CRUD를 내는 메시지에 `domain`이 없으면 **생성 실패**. 나중에 런타임에서 터지는 것보다
  코드 생성에서 터지는 쪽이 항상 싸다.
- 같은 앱 안에서 도메인 번호가 겹치면 **생성 실패**. 플러그인은 생성 요청의 모든 파일을
  보므로 알 수 있다.
- `0`은 거절.

### 3.3 엔티티마다 함수를 정의하지 않는다

앞선 검토에서 `Minter`를 `Scope`처럼 엔티티당 메서드 하나로 그렸는데, **그럴 이유가
없다.** 이유를 나누면 이렇다.

`Scope`가 엔티티마다 메서드를 갖는 것은 게으름이 아니라 **술어의 타입이 엔티티마다
다르기** 때문이다 — `predicate.Holder`와 `predicate.Tenant`는 다른 타입이라 하나의
시그니처로 담을 수 없다. go-app의 PLAN이 "Predicates are typed per entity, so one option
cannot carry them all"이라고 적은 것이 그 뜻이다.

키는 그렇지 않다. **모든 엔티티의 키가 같은 타입(`uuid.UUID`)이다.** 엔티티마다 달라지는
것은 숫자 하나뿐이고, 그 숫자는 **코드 생성 시점에 이미 알려져 있다**(§3.2). 그러니 훅은
하나면 된다.

```go
// protoc-gen-orm-ent/runtime — payday를 모른다
type Minter interface {
    Mint(ctx context.Context, entity string, given uuid.UUID, ok bool) (uuid.UUID, error)
}
```

생성된 `Add`는 자기 메시지의 전체 이름을 넘긴다. 생성기가 이미 아는 값이다:

```go
k, err := mintKey(ctx, s.Mint, "go_app.Robot", req.GetId(), req.HasId())
if err != nil {
    return nil, err
}
q.SetID(k)
```

payday 쪽 구현은 등록표를 한 번 보는 것이 전부다:

```go
func (m minter) Mint(ctx context.Context, entity string, given uuid.UUID, ok bool) (uuid.UUID, error) {
    d, found := LookupDomain(entity)   // domain.g.go의 init이 채운 표
    if !found {
        return uuid.Nil, fmt.Errorf("%s: no domain declared", entity)
    }
    if !ok {
        return New(d).Uuid(), nil
    }
    if got, _ := Of(given); got != d {
        return uuid.Nil, status.Errorf(codes.InvalidArgument,
            "id: names a %s, and this is a %s", got, d)
    }
    return given, nil
}
```

배선은 한 줄이다.

```go
sink, err := bare.NewServer(db, bare.WithMinter(pdid.Minter()))
```

### 3.4 생성기 변경은 payday를 모른다

위 설계의 값어치는 여기 있다. `protoc-gen-orm-ent`에 들어가는 변경은 **"키를 훅이 만들
수 있고, 훅에는 엔티티의 전체 이름이 넘어간다"** 뿐이다. 도메인도, UUID의 레이아웃도,
payday라는 이름도 나오지 않는다. 그러면:

- **포크가 아니라 상류 변경이 된다.** 유지할 포크가 하나 줄고, 다른 사용자에게도 쓸모가
  있다(결정적 ID로 테스트 픽스처를 고정하는 것만 해도).
- payday의 proto 옵션과 플러그인은 온전히 payday의 것이다. 게시를 기다릴 것이 없다.

`Minter`가 `nil`이면 지금처럼 `uuid.New()`로 떨어진다. 기존 사용자는 아무것도 바뀌지
않는다.

### 3.5 읽는 쪽도 검사한다

`Add`만 막으면 절반이다. 생성된 `<E>GetKey`가 `uuid.FromBytes(ref.GetId())`로 참조를
푸는데, 여기서 도메인이 다르면 지금은 쿼리까지 가서 `NotFound`가 된다. 같은 훅이 읽는
쪽에도 걸리면 DB 이전에 `InvalidArgument`이고, 메시지가 "이건 holder다"라고 말할 수
있다.

이것이 도메인 태그가 실제로 값을 내는 두 자리 중 하나다. 다른 하나는 감사 로그다 —
go-app의 `audit.proto`가 "행이 지워지면 그것이 무엇이었는지 말할 방법이 없다"를 미해결
비용으로 적어 두었는데, 식별자가 도메인을 들고 있으면 **읽을 필요가 없다.**

### 3.6 사람이 읽는 쪽 — slug

oasys의 `slug` 형식을 가져온다.

```
@[TENANT/]ALIAS[#DOMAIN]

@acme/admin#holder     테넌트 acme의 Holder "admin"
@acme/admin            도메인은 문맥에서 (HolderRef 자리라면 holder)
@acme#tenant           테넌트 자신 — 위에 테넌트가 없다
admin                  테넌트도 문맥에서 (호출자의 것)
```

**`@`가 있는 이유**는 장식이 아니다. 별칭 문법(`acme-corp`)은 UUID의 문자열 표기
(`0199c3f4-2a10-...`)와 겹친다 — 후자도 소문자 영숫자와 하이픈이다. 하나의 텍스트 필드가
"id 또는 alias"를 받아야 할 때 `@`가 그 둘을 가른다.

**`#domain`은 §3의 도메인 바이트와 같은 것이고 같은 선언에서 나온다.** `entity` 옵션의
`domain`(숫자)과 `name`(문자)이 짝이고, 플러그인이 한 번에 등록한다. 그러니 slug의
도메인 불일치는 ID의 도메인 불일치와 똑같이 취급된다 — 생략하면 문맥에서 추론하고,
적었는데 다르면 `InvalidArgument`. 주장이지 이름이 아니다.

#### 어디에 쓰이나

여기가 값을 내는 자리들이고, 전부 지금 임시방편으로 채워져 있다.

| 자리 | 지금 | slug로 |
| --- | --- | --- |
| `auth.Plain` 헤더 | `Plain <tenant>/<holder>` — 손으로 자름 | `Plain @acme/admin` |
| `auth.MTLS` 인증서 URI SAN | CN 또는 URI를 앱마다 다르게 읽음 | `payday://acme/admin#holder` |
| 설정의 토큰 저장소 키 | `acme/admin:` (YAML 키) | 같은 문자열, 파싱이 하나 |
| CLI, 로그, 오류 메시지 | UUID 또는 임시 문자열 | `@acme/admin#holder` |
| 감사 로그 렌더링 | `object_id`가 UUID | 도메인을 알면 slug로 되돌릴 수 있다 |

**와이어에는 넣지 않는다.** `HolderRef`는 지금처럼 구조화된 oneof(`id` 또는
`{alias, tenant}`)로 둔다 — 서버가 매 요청마다 텍스트를 해석하는 것은 같은 정보를 이미
타입으로 들고 있는데 다시 파싱하는 것이다. slug는 **경계**의 형식이다: 헤더, 설정,
인증서, 사람. 클라이언트 쪽 `slug.Ref(s) (*HolderRef, error)` 헬퍼로 잇는다.

#### 가져오면서 정할 것

**문법을 하나로 합쳐야 한다.** 두 저장소가 다르다.

| | 시작 | 허용 문자 | 길이 |
| --- | --- | --- | --- |
| oasys `slug.Validate` | 소문자 영문 | `[a-z0-9-_]`, 연속 `--`/`__` 금지 | 없음 |
| go-app `ParseAlias` | 영문 또는 숫자 | `[a-z0-9]`와 하이픈 | 63 |

**oasys 쪽(영문으로 시작)에 go-app 쪽(하이픈만, 63자)을 합치는 것을 권한다.**

- *영문으로 시작*은 oasys가 맞다. 숫자로 시작할 수 있으면 사람이 읽는 참조가 숫자
  식별자처럼 보인다.
- *밑줄 없음*은 go-app이 맞다. `_`를 허용하면 별칭을 DNS 라벨이나 서브도메인으로 쓸
  길이 닫힌다. 지금 필요 없더라도 닫아 둘 이유가 없다.
- *63자*는 같은 이유로 go-app이 맞다.
- 정규화(trim + 소문자)는 go-app에만 있다. 가져온다 — `" Acme "`와 `acme`가 같은 것을
  가리켜야 한다.

**그리고 고칠 것 셋.**

1. **`WithDomain`이 `#`를 빠뜨린다.** `a[:i] + Slug(v.String())`이라서
   `WithDomain("acme/admin", Robot)`이 `"acme/adminrobot"`이 된다.
2. **`NewN`의 첫 글자가 `z`를 못 낸다.** `Charset[int(v0)%('z'-'a')]`에서 `'z'-'a'`는
   25라 `a`..`y`만 나온다. 26이어야 한다.
3. **`FromBase`의 두 경로가 알파벳이 다르다.** base32 경로는 `i`/`l`/`o`를 내고, 짧을
   때 쓰는 `encodeBase24`는 그것들을 뺀다. 사람이 옮겨 적는 문자열에서 혼동 문자를 빼는
   것은 옳으므로 **한쪽으로 통일한다** — 혼동 문자를 뺀 알파벳 하나로.

`Parse`가 `@`를 떼는데 `String()`이 다시 붙이지 않는 비대칭도 정리한다. 왕복이 같은
문자열이어야 한다.

