package biz

import (
	"context"
	"time"

	"github.com/6tail/lunar-go/calendar"
	"github.com/go-kratos/kratos/v2/errors"
)

// TodayWellness is a generic seasonal card, not a constitution-based recommendation.
type TodayWellness struct {
	Date      string
	SolarTerm string
	Advice    string
}

type WellnessUsecase interface {
	GetTodayWellness(ctx context.Context) (*TodayWellness, error)
}

type wellnessUsecase struct {
	now func() time.Time
}

func NewWellnessUsecase() WellnessUsecase {
	return &wellnessUsecase{now: time.Now}
}

// Fixed UTC+8 is the product's Beijing-time rule. It does not depend on the
// host timezone, installed tzdata, or historical daylight saving rules.
var wellnessLocation = time.FixedZone("UTC+8", 8*60*60)

func (uc *wellnessUsecase) GetTodayWellness(ctx context.Context) (*TodayWellness, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Read the clock only once so the date and term cannot straddle midnight.
	today := uc.now().In(wellnessLocation)
	lunar := calendar.NewSolarFromYmd(today.Year(), int(today.Month()), today.Day()).GetLunar()
	// Whole-day matching includes today's term even before its astronomical
	// transition time, and includes the preceding winter solstice in January.
	term := lunar.GetPrevJieQiByWholeDay(true)
	if term == nil {
		return nil, errors.InternalServer("WELLNESS_UNAVAILABLE", "current solar term is unavailable")
	}
	advice, ok := wellnessAdvice[term.GetName()]
	if !ok {
		return nil, errors.InternalServer("WELLNESS_UNAVAILABLE", "solar term advice is unavailable")
	}
	return &TodayWellness{
		Date:      today.Format(time.DateOnly),
		SolarTerm: term.GetName(),
		Advice:    advice,
	}, nil
}

// Editorial content: one short, general lifestyle suggestion per solar term.
// Changes ship with code; no user data, medical claims, or runtime generation.
var wellnessAdvice = map[string]string{
	"立春": "早春气温多变，注意适时添衣，天气适宜时散步舒展身体。",
	"雨水": "雨水渐多，外出留意天气变化，饮食多样搭配，保持规律作息。",
	"惊蛰": "春日渐暖，可逐步增加户外活动，量力而行，避免突然剧烈运动。",
	"春分": "保持规律作息，饮食均衡，久坐之余起身活动，享受春日阳光。",
	"清明": "踏青时注意天气和花粉情况，适度步行，外出归来及时清洁。",
	"谷雨": "暮春注意随气温增减衣物，选择新鲜时蔬，保持饮食多样。",
	"立夏": "天气渐热，注意及时饮水，午后适当休息，户外活动避开暴晒。",
	"小满": "气温升高，饮食注意清洁与均衡，少量多次饮水，保持适度活动。",
	"芒种": "暑热渐起，外出做好防晒，出汗后及时补水，夜间保持规律睡眠。",
	"夏至": "白昼较长，仍应按时入睡，户外运动尽量安排在凉爽时段。",
	"小暑": "高温天气减少长时间户外活动，注意通风降温，及时补充水分。",
	"大暑": "酷暑时留意高温预警，避免烈日下运动，饮食清洁，充分休息。",
	"立秋": "初秋仍有暑热，继续做好防晒补水，饮食适量，不必集中进补。",
	"处暑": "暑气渐退，早晚温差增大，按体感增减衣物，逐步恢复户外活动。",
	"白露": "早晚渐凉，外出备好薄外套，注意饮水，保持室内空气流通。",
	"秋分": "秋日注意规律作息，适量吃蔬果和全谷物，安排轻松的日常运动。",
	"寒露": "气温下降，清晨外出注意保暖，运动前充分热身，避免勉强加量。",
	"霜降": "秋凉渐深，注意添衣和居室通风，饮食均衡，保持适度身体活动。",
	"立冬": "入冬注意保暖，保持规律睡眠，合理搭配饮食，不以进补代替均衡。",
	"小雪": "天气转冷，室内也可适度活动，使用取暖设备时注意通风和安全。",
	"大雪": "寒冷天气减少久坐，选择安全的室内运动，外出注意保暖防滑。",
	"冬至": "冬日保持规律作息，聚餐注意适量，白天天气适宜时出门走走。",
	"小寒": "严寒时注意头颈和手足保暖，晨练不必过早，运动以舒适为宜。",
	"大寒": "岁末天寒，注意防寒与室内通风，饮食多样，安排充足休息。",
}
