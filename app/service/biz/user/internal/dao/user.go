// Copyright 2022 Teamgram Authors
//  All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
//
// Author: teamgramio (teamgram.io@gmail.com)
//

package dao

import (
	"context"
	"github.com/teamgram/marmota/pkg/threading2"
	"math/rand"
	"strconv"
	"time"

	"github.com/teamgram/marmota/pkg/container2"
	"github.com/teamgram/marmota/pkg/stores/sqlx"
	"github.com/teamgram/proto/mtproto"
	"github.com/teamgram/teamgram-server/app/service/biz/user/internal/dal/dataobject"
	"github.com/teamgram/teamgram-server/app/service/biz/user/user"

	"github.com/zeromicro/go-zero/core/logx"
	"github.com/zeromicro/go-zero/core/mr"
)

func (d *Dao) getBotData(ctx context.Context, botId int64) *user.BotData {
	var (
		botData *user.BotData
	)

	botDO, _ := d.BotsDAO.Select(ctx, botId)
	if botDO != nil {
		// userData.Bot
		botData = user.MakeTLBotData(&user.BotData{
			Id:                   botDO.BotId,
			BotType:              botDO.BotType,
			Creator:              botDO.CreatorUserId,
			Token:                botDO.Token,
			Description:          botDO.Description,
			BotChatHistory:       botDO.BotChatHistory,
			BotNochats:           botDO.BotNochats,
			BotInlineGeo:         botDO.BotInlineGeo,
			BotInfoVersion:       botDO.BotInfoVersion,
			BotInlinePlaceholder: mtproto.MakeFlagsString(botDO.BotInlinePlaceholder),
		}).To_BotData()
	}

	return botData
}

func (d *Dao) CreateNewUserV2(
	ctx context.Context,
	secretKeyId int64,
	phone string,
	countryCode string,
	firstName string, lastName string) (*user.ImmutableUser, error) {
	var (
		//err    error
		userDO        *dataobject.UsersDO
		now           = time.Now().Unix()
		cacheUserData = NewCacheUserData()
	)

	//
	tR := sqlx.TxWrapper(ctx, d.DB, func(tx *sqlx.Tx, result *sqlx.StoreResult) {
		// var err error
		// user
		userDO = &dataobject.UsersDO{
			UserType:       user.UserTypeRegular,
			AccessHash:     rand.Int63(),
			Phone:          phone,
			SecretKeyId:    secretKeyId,
			FirstName:      firstName,
			LastName:       lastName,
			CountryCode:    countryCode,
			AccountDaysTtl: 180,
		}
		if lastInsertId, _, err2 := d.UsersDAO.InsertTx(tx, userDO); err2 != nil {
			if sqlx.IsDuplicate(err2) {
				result.Err = mtproto.ErrPhoneNumberOccupied
				return
			}
			result.Err = err2
			return
		} else {
			userDO.Id = lastInsertId
		}
		cacheUserData.UserData = d.makeUserDataByDO(userDO)

		result.Err = mr.Finish(
			func() error {
				// presences
				presencesDO := &dataobject.UserPresencesDO{
					UserId:     userDO.Id,
					LastSeenAt: now,
					Expires:    0,
				}

				if _, _, err2 := d.UserPresencesDAO.InsertTx(tx, presencesDO); err2 != nil {
					return err2
				}

				return nil
			},
			func() error {
				// privacy
				//bData, _ := json.Marshal(defaultRules)
				//bData2, _ := json.Marshal(phoneNumberRules)
				doList := make([]*dataobject.UserPrivaciesDO, 0, user.MAX_KEY_TYPE)
				for i := user.STATUS_TIMESTAMP; i <= user.MAX_KEY_TYPE; i++ {
					if i == user.PHONE_NUMBER {
						doList = append(doList, &dataobject.UserPrivaciesDO{
							Id:      1,
							UserId:  userDO.Id,
							KeyType: int32(i),
							Rules:   phoneNumberRulesData,
						})

						cacheUserData.CachesPrivacyKeyRules = append(
							cacheUserData.CachesPrivacyKeyRules,
							user.MakeTLPrivacyKeyRules(&user.PrivacyKeyRules{
								Key:   int32(i),
								Rules: phoneNumberRules,
							}).To_PrivacyKeyRules())
					} else {
						doList = append(doList, &dataobject.UserPrivaciesDO{
							Id:      1,
							UserId:  userDO.Id,
							KeyType: int32(i),
							Rules:   defaultRulesData,
						})

						if i == user.PROFILE_PHOTO ||
							i == user.PHONE_NUMBER {
							cacheUserData.CachesPrivacyKeyRules = append(
								cacheUserData.CachesPrivacyKeyRules,
								user.MakeTLPrivacyKeyRules(&user.PrivacyKeyRules{
									Key:   int32(i),
									Rules: defaultRules,
								}).To_PrivacyKeyRules())
						}
					}
				}

				logx.WithContext(ctx).Infof("doList - %v", doList)
				_, _, err2 := d.UserPrivaciesDAO.InsertBulkTx(tx, doList)
				if err2 != nil {
					return err2
				}

				return nil
			},
			func() error {
				_, _, err2 := d.UserGlobalPrivacySettingsDAO.InsertOrUpdateTx(tx, &dataobject.UserGlobalPrivacySettingsDO{
					UserId:                           userDO.Id,
					ArchiveAndMuteNewNoncontactPeers: false,
				})
				if err2 != nil {
					return err2
				}

				return nil
			})
	})

	if tR.Err != nil {
		logx.WithContext(ctx).Errorf("createNewUser2 error: %v", tR.Err)
		return nil, tR.Err
	}

	return threading2.WrapperGoFunc(
		ctx,
		user.MakeTLImmutableUser(&user.ImmutableUser{
			User:             cacheUserData.UserData,
			LastSeenAt:       now,
			Contacts:         nil,
			KeysPrivacyRules: nil,
		}).To_ImmutableUser(),
		func(ctx context.Context) {
			//
			d.CachedConn.SetCache(ctx, genCacheUserDataCacheKey(userDO.Id), cacheUserData)
		}).(*user.ImmutableUser), nil
}

func (d *Dao) UpdateUserFirstAndLastName(ctx context.Context, id int64, firstName, lastName string) bool {
	_, _, err := d.CachedConn.Exec(
		ctx,
		func(ctx context.Context, conn *sqlx.DB) (int64, int64, error) {
			rowsAffected, err := d.UsersDAO.UpdateUser(ctx, map[string]interface{}{
				"first_name": firstName,
				"last_name":  lastName,
			}, id)

			if err != nil {
				return 0, 0, err
			}

			return 0, rowsAffected, nil
		},
		genCacheUserDataCacheKey(id))
	if err != nil {
		logx.WithContext(ctx).Errorf("updateUserFirstAndLastName - error: %v", err)
		return false
	}

	return true
}

func (d *Dao) UpdateUserAbout(ctx context.Context, id int64, about string) bool {
	_, _, err := d.CachedConn.Exec(
		ctx,
		func(ctx context.Context, conn *sqlx.DB) (int64, int64, error) {
			rowsAffected, err := d.UsersDAO.UpdateUser(ctx, map[string]interface{}{
				"about": about,
			}, id)

			if err != nil {
				return 0, 0, err
			}

			return 0, rowsAffected, nil
		},
		genCacheUserDataCacheKey(id))
	if err != nil {
		logx.WithContext(ctx).Errorf("updateUserAbout - error: %v", err)
		return false
	}

	return true
}

func (d *Dao) UpdateUserUsername(ctx context.Context, id int64, username string) bool {
	_, _, err := d.CachedConn.Exec(
		ctx,
		func(ctx context.Context, conn *sqlx.DB) (int64, int64, error) {
			rowsAffected, err := d.UsersDAO.UpdateUser(ctx, map[string]interface{}{
				"username": username,
			}, id)

			if err != nil {
				return 0, 0, err
			}

			return 0, rowsAffected, nil
		},
		genCacheUserDataCacheKey(id))
	if err != nil {
		logx.WithContext(ctx).Errorf("updateUserUsername - error: %v", err)
		return false
	}

	return true
}

func (d *Dao) UpdateProfilePhoto(ctx context.Context, userId, photoId int64) int64 {
	var (
		mainPhotoId = photoId
	)

	_, _, err := d.CachedConn.Exec(
		ctx,
		func(ctx context.Context, conn *sqlx.DB) (int64, int64, error) {
			var err error
			if photoId == 0 {
				mainPhotoId, _ = d.UsersDAO.SelectProfilePhoto(ctx, userId)
				if mainPhotoId > 0 {
					nextPhotoId, _ := d.UserProfilePhotosDAO.SelectNext(ctx, userId, []int64{mainPhotoId})
					tR := sqlx.TxWrapper(ctx, d.DB, func(tx *sqlx.Tx, result *sqlx.StoreResult) {
						_, result.Err = d.UserProfilePhotosDAO.DeleteTx(tx, userId, []int64{mainPhotoId})
						if result.Err != nil {
							return
						}
						_, result.Err = d.UsersDAO.UpdateProfilePhotoTx(tx, nextPhotoId, userId)
					})
					err = tR.Err
				}
			} else {
				tR := sqlx.TxWrapper(ctx, d.DB, func(tx *sqlx.Tx, result *sqlx.StoreResult) {
					_, _, result.Err = d.UserProfilePhotosDAO.InsertOrUpdateTx(tx, &dataobject.UserProfilePhotosDO{
						UserId:  userId,
						PhotoId: mainPhotoId,
						Date2:   time.Now().Unix(),
					})
					if result.Err != nil {
						return
					}
					_, result.Err = d.UsersDAO.UpdateProfilePhotoTx(tx, mainPhotoId, userId)
				})
				err = tR.Err
			}

			return 0, 0, err
		},
		genCacheUserDataCacheKey(userId))
	if err != nil {
		logx.WithContext(ctx).Errorf("updateProfilePhoto - error: %v", err)
		return 0
	}

	return mainPhotoId
}

func (d *Dao) GetImmutableUser(ctx context.Context, id int64, privacy bool, contacts ...int64) (*user.ImmutableUser, error) {
	cacheUserData := d.GetCacheUserData(ctx, id)

	// userDO, _ := c.svcCtx.Dao.UsersDAO.SelectById(c.ctx, in.Id)
	if cacheUserData == nil {
		err := mtproto.ErrUserIdInvalid
		logx.WithContext(ctx).Errorf("user.getImmutableUser - error: %v", err)
		return nil, err
	}
	userData := cacheUserData.UserData
	immutableUser := user.MakeTLImmutableUser(&user.ImmutableUser{
		User:             userData,
		LastSeenAt:       0,
		Contacts:         nil,
		KeysPrivacyRules: nil,
	}).To_ImmutableUser()

	if !userData.Deleted {
		if int(userData.UserType) == user.UserTypeRegular {
			mr.FinishVoid(
				func() {
					lastSeenAt, _ := d.GetLastSeenAt(ctx, id)
					if lastSeenAt != nil {
						immutableUser.LastSeenAt = lastSeenAt.LastSeenAt
					}
				},
				func() {
					// TODO: aaa
					// immutableUser.Contacts = c.svcCtx.Dao.GetUserContactListByIdList(c.ctx, id, contacts...)

					idList := cacheUserData.GetContactIdList()
					if len(idList) == 0 {
						return
					}

					idList2 := make([]int64, 0, len(idList))
					for _, id2 := range contacts {
						if ok, _ := container2.Contains(id2, idList); ok && id2 != id {
							idList2 = append(idList2, id2)
						}
					}
					if len(idList2) == 0 {
						return
					}

					immutableUser.Contacts = d.getContactListByIdList(ctx, id, idList2)
				})
			//func() {
			//	if privacy {
			//		immutableUser.KeysPrivacyRules = c.svcCtx.Dao.GetUserPrivacyRulesListByKeys(
			//			c.ctx,
			//			id,
			//			user.STATUS_TIMESTAMP,
			//			user.PROFILE_PHOTO,
			//			user.PHONE_NUMBER)
			//	}
			//})
			if privacy {
				immutableUser.KeysPrivacyRules = cacheUserData.CachesPrivacyKeyRules
			}
		}
	}

	return immutableUser, nil
}

func (d *Dao) UpdateUserEmojiStatus(ctx context.Context, id int64, emojiStatusDocumentId int64, emojiStatusUntil int32) bool {
	_, _, err := d.CachedConn.Exec(
		ctx,
		func(ctx context.Context, conn *sqlx.DB) (int64, int64, error) {
			rowsAffected, err := d.UsersDAO.UpdateEmojiStatus(
				ctx,
				emojiStatusDocumentId,
				emojiStatusUntil,
				id)

			if err != nil {
				return 0, 0, err
			}

			return 0, rowsAffected, nil
		},
		genCacheUserDataCacheKey(id))
	if err != nil {
		logx.WithContext(ctx).Errorf("updateUserEmojiStatus - error: %v", err)
		return false
	}

	return true
}

func (d *Dao) DeleteUser(ctx context.Context, id int64, reason string) bool {
	_, _, err := d.CachedConn.Exec(
		ctx,
		func(ctx context.Context, conn *sqlx.DB) (int64, int64, error) {
			rowsAffected, err := d.UsersDAO.Delete(
				ctx,
				"-"+strconv.FormatInt(id, 10), // hack
				reason,
				id)
			if err != nil {
				return 0, 0, err
			}

			return 0, rowsAffected, nil
		},
		genCacheUserDataCacheKey(id))
	if err != nil {
		logx.WithContext(ctx).Errorf("DeleteUser - error: %v", err)
		return false
	}

	return true
}
