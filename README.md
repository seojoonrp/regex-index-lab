# regex-index-lab

태그 prefix 검색에서 case-insensitive regex가 인덱스를 무력화하는 걸 수치로 확인해보는 벤치마크 프로젝트
당밤공을 제작하는 과정에서 발견한 성능 최적화 포인트가 진짜로 유의미한지, 의미 있다면 얼마나 다른지 궁금해져서 시작했다.

## 문제 상황

유저마다 영어 대문자 10글자로 이루어진 태그를 가지고 있고, 태그는 unique index가 걸려 있는 상태다.
사용자가 대소문자 구분 없는 영어 문자열을 입력하면, 이를 통해 prefix 검색한 결과를 반환해야 하는 상황이다.

두 가지 구현 방식이 있다.

- **i-regex** : `{tag: {$regex: "^"+prefix, $options: "i"}}`
- **sensitive** : 입력을 `ToUpper` 후 `{tag: {$regex: "^"+QuoteMeta(prefix)}}`

이때 i-regex 방식에 문제가 있다. MongoDB는 `^` prefix + case-sensitive regex일 때만 인덱스 range scan을 쓴다.
여기에 `i` 옵션을 붙이면 대소문자가 인덱스 상에서 흩어져 있어 범위로 묶지 못하고 인덱스 전체를 훑기에 성능이 저하된다.
본 프로젝트에서는 두 방식의 성능 차이가 얼마나 나는지 검증한다.

## 측정 방식

세 가지를 독립변수로 둔다.

- **문서 수 N**(1000 / 10000 / 100000)
- **prefix 길이**(2글자, 3글자, 10글자) - 2, 3글자는 사용자가 직접 입력하는 일반적인 경우,
  10글자는 태그를 복사해 검색하는 경우에 대응된다.
- **조회 approach**(i-regex / sensitive)

각 (N, prefix 길이) 케이스마다 두 가지를 측정한다.

- **explain** : 부하와 분리해 대표 prefix로 approach당 한 번 실행한다. `executionStats`에서 totalKeysExamined,
  totalDocsExamined, nReturned를 기록한다.
- **latency** : worker 8개로 부하를 걸어 요청별 소요시간을 모으고 p50/p95/p99를 뽑는다. 평균
  대신 percentile을 보는 건 스캔 비용이 tail에서 두드러지기 때문이다.

수치가 approach 차이만 반영하도록 아래 사항들을 지켰다.

- 태그는 A–Z 10글자 랜덤으로 N개 생성하고 unique index를 건다.
- prefix는 풀에서 매 요청 바꿔가며 쓴다. 2/3글자는 랜덤으로 뽑고, 10글자 exact는 실제 저장된 태그를 샘플링한다.
- 네트워크 지연과 같은 노이즈를 제거하고, 쿼리 approach의 차이만 보기 위해 부하 생성과 mongod를 같은 머신에서 돌린다.
- 초기 쿼리는 캐시 적재 과정으로 인해 느리기 때문에 워밍업 한 바퀴를 돌리고 버린다.
- prefix를 매 요청 바꾸고 두 방식을 요청 단위로 번갈아 돌린다. 한 prefix만 때리면 캐싱에
  가려지고, 한 방식을 몰아 돌리면 머신 상태 변동이 공평하지 않을 수 있다.

## 측정 결과 분석

N=100000 실행 결과:

| prefix | approach  | keysExamined | nReturned | p50(ms) | p95(ms) |
| ------ | --------- | -----------: | --------: | ------: | ------: |
| 2글자  | i-regex   |      100,000 |       132 |    88.8 |   125.8 |
| 2글자  | sensitive |          133 |       132 |     1.9 |     2.8 |
| 10글자 | i-regex   |      100,000 |         1 |    86.1 |    97.0 |
| 10글자 | sensitive |            1 |         1 |     0.8 |     1.0 |

i-regex는 prefix를 10글자로 완전히 지정해도 인덱스 키 10만 개를 전부 검사함을 확인할 수 있다.
문서 하나 찾자고 전체를 훑는 비효율적인 탐색이다. 반면 sensitive는 range로 바로 짚어서 키 1개만 본다.

N을 키우면 차이가 더 분명해진다 (10글자 exact 기준 p50):

| N       | i-regex keys | i-regex p50 | sensitive keys | sensitive p50 |
| ------- | -----------: | ----------: | -------------: | ------------: |
| 1,000   |        1,000 |       1.8ms |              2 |         0.8ms |
| 10,000  |       10,000 |       9.4ms |              2 |         0.7ms |
| 100,000 |      100,000 |      86.1ms |              2 |         0.8ms |

i-regex의 p50 latency는 N에 비례해 늘어나지만(사실상 O(N)), sensitive는 N과 무관하게 비슷하다.

## 실행

로컬 MongoDB를 띄운 후 실행하면 된다.
끝나면 `results.csv`에 벤치마크가 저장되고, 콘솔에는 케이스별 explain 결과가 찍힌다.

```bash
docker run -d --name mongo -p 27017:27017 mongo:7
go run .
```

## 구성

- `main.go` : 오케스트레이션, CSV 출력
- `database.go` : MongoDB 연결, tag index 생성
- `seed.go` : N개 태그 문서 생성
- `queries.go` : 두 approach의 필터 builder
- `benchmark.go` : 실제 쿼리 실행 및 결과 저장
